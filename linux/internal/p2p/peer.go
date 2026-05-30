// Package p2p does direct, peer-to-peer file transfer over a WebRTC DataChannel.
// File bytes never touch the relay: it carries only the (E2E-encrypted) SDP/ICE
// signaling. Same-LAN peers get a direct local link at full speed; across networks
// ICE hole-punches via STUN (TURN optional). See proto/PROTOCOL.md §7.
//
// Trickle ICE: each side sends its offer/answer immediately, then streams ICE
// candidates as they're gathered (candidates received before the remote SDP is
// applied are queued). The transfer is message-framed (a JSON head, binary chunks,
// a JSON done) so the Go and Android sides interoperate.
package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	chunkSize    = 16 * 1024
	maxBuffered  = 1 << 20
	channelLabel = "linuxdrop-file"
	sendTimeout  = 15 * time.Minute
)

// Signal is the WebRTC negotiation payload, sealed inside a relay `signal` frame.
type Signal struct {
	Kind          string  `json:"kind"` // "offer" | "answer" | "candidate"
	SDP           string  `json:"sdp,omitempty"`
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        string  `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
	Origin        string  `json:"origin,omitempty"`
}

type head struct {
	T    string `json:"t"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mime string `json:"mime"`
}

type ctrl struct {
	T      string `json:"t"` // "done" | "ok" | "err"
	Sha256 string `json:"sha256,omitempty"`
	Msg    string `json:"msg,omitempty"`
}

// SendSignalFunc seals + sends a signal payload to a peer dev id via the relay.
type SendSignalFunc func(toDev string, payload []byte)

// session is one peer connection with trickle-ICE candidate queueing.
type session struct {
	pc        *webrtc.PeerConnection
	mu        sync.Mutex
	remoteSet bool
	pending   []webrtc.ICECandidateInit
}

func (s *session) addCandidate(c webrtc.ICECandidateInit) {
	s.mu.Lock()
	if !s.remoteSet {
		s.pending = append(s.pending, c)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = s.pc.AddICECandidate(c)
}

func (s *session) setRemote(desc webrtc.SessionDescription) error {
	if err := s.pc.SetRemoteDescription(desc); err != nil {
		return err
	}
	s.mu.Lock()
	s.remoteSet = true
	pend := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, c := range pend {
		_ = s.pc.AddICECandidate(c)
	}
	return nil
}

type Manager struct {
	dev         string
	iceBase     string
	downloadDir string
	log         *log.Logger
	sendSignal  SendSignalFunc
	onReceived  func(path string)

	mu    sync.Mutex
	peers map[string]*session
	api   *webrtc.API
}

func NewManager(dev, relayURL, downloadDir string, logger *log.Logger) *Manager {
	return &Manager{
		dev:         dev,
		iceBase:     wsToHTTP(relayURL),
		downloadDir: downloadDir,
		log:         logger,
		peers:       map[string]*session{},
		api:         webrtc.NewAPI(),
	}
}

func (m *Manager) SetSendSignal(f SendSignalFunc)   { m.sendSignal = f }
func (m *Manager) SetOnReceived(f func(path string)) { m.onReceived = f }

func wsToHTTP(u string) string {
	u = strings.TrimRight(u, "/")
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	default:
		return u
	}
}

func (m *Manager) track(dev string, s *session) {
	m.mu.Lock()
	if old := m.peers[dev]; old != nil {
		_ = old.pc.Close()
	}
	m.peers[dev] = s
	m.mu.Unlock()
}

func (m *Manager) get(dev string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peers[dev]
}

func (m *Manager) untrack(dev string) {
	m.mu.Lock()
	delete(m.peers, dev)
	m.mu.Unlock()
}

func (m *Manager) emit(toDev string, sig Signal) {
	if m.sendSignal == nil {
		return
	}
	sig.Origin = m.dev
	b, _ := json.Marshal(sig)
	m.sendSignal(toDev, b)
}

func (m *Manager) emitCandidate(toDev string, c *webrtc.ICECandidate) {
	if c == nil {
		return
	}
	j := c.ToJSON()
	sig := Signal{Kind: "candidate", Candidate: j.Candidate, SDPMLineIndex: j.SDPMLineIndex}
	if j.SDPMid != nil {
		sig.SDPMid = *j.SDPMid
	}
	m.emit(toDev, sig)
}

func (m *Manager) iceServers() []webrtc.ICEServer {
	def := []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	resp, err := http.Get(m.iceBase + "/ice")
	if err != nil {
		return def
	}
	defer resp.Body.Close()
	var out struct {
		IceServers []struct {
			URLs       any    `json:"urls"`
			Username   string `json:"username"`
			Credential string `json:"credential"`
		} `json:"iceServers"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return def
	}
	var servers []webrtc.ICEServer
	for _, s := range out.IceServers {
		var urls []string
		switch v := s.URLs.(type) {
		case string:
			urls = []string{v}
		case []any:
			for _, u := range v {
				if us, ok := u.(string); ok {
					urls = append(urls, us)
				}
			}
		}
		if len(urls) == 0 {
			continue
		}
		ice := webrtc.ICEServer{URLs: urls}
		if s.Username != "" {
			ice.Username = s.Username
			ice.Credential = s.Credential
		}
		servers = append(servers, ice)
	}
	if len(servers) == 0 {
		return def
	}
	return servers
}

func (m *Manager) newPC() (*webrtc.PeerConnection, error) {
	return m.api.NewPeerConnection(webrtc.Configuration{ICEServers: m.iceServers()})
}

// OnSignal handles an inbound (already-decrypted) signal payload from fromDev.
func (m *Manager) OnSignal(fromDev string, payload []byte) {
	var sig Signal
	if json.Unmarshal(payload, &sig) != nil {
		return
	}
	switch sig.Kind {
	case "offer":
		m.handleOffer(fromDev, sig.SDP)
	case "answer":
		if s := m.get(fromDev); s != nil {
			if err := s.setRemote(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sig.SDP}); err != nil {
				m.log.Printf("p2p: set answer: %v", err)
			}
		}
	case "candidate":
		if s := m.get(fromDev); s != nil {
			init := webrtc.ICECandidateInit{Candidate: sig.Candidate, SDPMLineIndex: sig.SDPMLineIndex}
			if sig.SDPMid != "" {
				mid := sig.SDPMid
				init.SDPMid = &mid
			}
			s.addCandidate(init)
		}
	}
}

// SendFile establishes a connection to toDev and streams path. Blocks until the
// receiver acknowledges completion (or it fails / times out).
func (m *Manager) SendFile(toDev, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}

	pc, err := m.newPC()
	if err != nil {
		return err
	}
	dc, err := pc.CreateDataChannel(channelLabel, nil)
	if err != nil {
		pc.Close()
		return err
	}
	sess := &session{pc: pc}
	m.track(toDev, sess)
	defer func() { pc.Close(); m.untrack(toDev) }()

	result := make(chan error, 1)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) { m.emitCandidate(toDev, c) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		m.log.Printf("p2p[%s]: %s", toDev, s)
		if s == webrtc.PeerConnectionStateFailed {
			select {
			case result <- fmt.Errorf("connection failed"):
			default:
			}
		}
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}
		var c ctrl
		_ = json.Unmarshal(msg.Data, &c)
		switch c.T {
		case "ok":
			select {
			case result <- nil:
			default:
			}
		case "err":
			select {
			case result <- fmt.Errorf("peer rejected: %s", c.Msg):
			default:
			}
		}
	})
	dc.OnOpen(func() {
		go sendAll(dc, f, head{T: "head", Name: filepath.Base(path), Size: st.Size(), Mime: mimeByExt(path)}, m.log)
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	m.emit(toDev, Signal{Kind: "offer", SDP: offer.SDP}) // trickle: send immediately
	m.log.Printf("p2p[%s]: offer sent (%s, %d bytes)", toDev, filepath.Base(path), st.Size())

	select {
	case err := <-result:
		if err == nil {
			m.log.Printf("p2p[%s]: transfer complete", toDev)
		}
		time.Sleep(200 * time.Millisecond)
		return err
	case <-time.After(sendTimeout):
		return fmt.Errorf("transfer timed out")
	}
}

func (m *Manager) handleOffer(fromDev, sdp string) {
	pc, err := m.newPC()
	if err != nil {
		m.log.Printf("p2p: newPC: %v", err)
		return
	}
	sess := &session{pc: pc}
	m.track(fromDev, sess)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) { m.emitCandidate(fromDev, c) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		m.log.Printf("p2p[%s]: %s", fromDev, s)
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			m.untrack(fromDev)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { m.receive(fromDev, dc) })

	if err := sess.setRemote(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		m.log.Printf("p2p: set offer: %v", err)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		m.log.Printf("p2p: create answer: %v", err)
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return
	}
	m.emit(fromDev, Signal{Kind: "answer", SDP: answer.SDP}) // trickle: send immediately
}

func (m *Manager) receive(fromDev string, dc *webrtc.DataChannel) {
	var (
		h        head
		tmp      *os.File
		hasher   = sha256.New()
		received int64
	)
	fail := func(reason string) {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			tmp = nil
		}
		b, _ := json.Marshal(ctrl{T: "err", Msg: reason})
		_ = dc.SendText(string(b))
		m.log.Printf("p2p[%s]: receive failed: %s", fromDev, reason)
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			var probe map[string]any
			_ = json.Unmarshal(msg.Data, &probe)
			switch probe["t"] {
			case "head":
				_ = json.Unmarshal(msg.Data, &h)
				t, err := os.CreateTemp(m.downloadDir, ".linuxdrop-*.part")
				if err != nil {
					fail("cannot open temp file")
					return
				}
				tmp = t
				hasher = sha256.New()
				received = 0
				m.log.Printf("p2p[%s]: receiving %q (%d bytes)", fromDev, h.Name, h.Size)
			case "done":
				if tmp == nil {
					return
				}
				var c ctrl
				_ = json.Unmarshal(msg.Data, &c)
				tmp.Close()
				got := hex.EncodeToString(hasher.Sum(nil))
				if received != h.Size || (c.Sha256 != "" && got != c.Sha256) {
					os.Remove(tmp.Name())
					fail("size/hash mismatch")
					tmp = nil
					return
				}
				final := uniquePath(m.downloadDir, h.Name)
				if err := os.Rename(tmp.Name(), final); err != nil {
					fail("cannot save file")
					tmp = nil
					return
				}
				tmp = nil
				_ = os.Chmod(final, 0o644)
				ok, _ := json.Marshal(ctrl{T: "ok"})
				_ = dc.SendText(string(ok))
				m.log.Printf("p2p[%s]: saved %s", fromDev, final)
				if m.onReceived != nil {
					m.onReceived(final)
				}
			}
			return
		}
		if tmp == nil {
			return
		}
		if _, err := tmp.Write(msg.Data); err != nil {
			fail("write error")
			return
		}
		hasher.Write(msg.Data)
		received += int64(len(msg.Data))
	})
}

func sendAll(dc *webrtc.DataChannel, f *os.File, h head, logger *log.Logger) {
	hb, _ := json.Marshal(h)
	if err := dc.SendText(string(hb)); err != nil {
		logger.Printf("p2p: send head: %v", err)
		return
	}
	hasher := sha256.New()
	buf := make([]byte, chunkSize)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			for dc.BufferedAmount() > maxBuffered {
				time.Sleep(5 * time.Millisecond)
			}
			if e := dc.Send(buf[:n]); e != nil {
				logger.Printf("p2p: send chunk: %v", e)
				return
			}
			hasher.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Printf("p2p: read file: %v", err)
			return
		}
	}
	db, _ := json.Marshal(ctrl{T: "done", Sha256: hex.EncodeToString(hasher.Sum(nil))})
	_ = dc.SendText(string(db))
}

func uniquePath(dir, name string) string {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
}

func mimeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}
