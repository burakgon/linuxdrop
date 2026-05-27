// Package p2p does direct, peer-to-peer file transfer over a WebRTC DataChannel.
// File bytes never touch the relay: it carries only the (E2E-encrypted) SDP
// signaling. Same-LAN peers get a direct local link at full speed; across networks
// ICE hole-punches via STUN (TURN optional). See proto/PROTOCOL.md §7.
//
// Negotiation is non-trickle for simplicity + cross-platform parity: each side
// gathers ICE fully, then sends one complete SDP (offer/answer). The transfer
// itself is message-framed (a JSON head, binary chunks, a JSON done) so the Go and
// Android sides interoperate without relying on byte-stream semantics.
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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	chunkSize    = 16 * 1024  // SCTP-safe DataChannel message size
	maxBuffered  = 1 << 20    // 1 MiB high-water mark for backpressure
	channelLabel = "bgn-file"
	sendTimeout  = 15 * time.Minute
)

// Signal is the WebRTC negotiation payload, sealed inside a relay `signal` frame.
type Signal struct {
	Kind   string `json:"kind"` // "offer" | "answer"
	SDP    string `json:"sdp"`
	Origin string `json:"origin,omitempty"`
}

// head is the first DataChannel message (text); chunks follow (binary); then done.
type head struct {
	T    string `json:"t"` // "head"
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

type Manager struct {
	dev         string
	iceBase     string // http(s) origin of the relay (GET /ice)
	downloadDir string
	log         *log.Logger
	sendSignal  SendSignalFunc

	mu    sync.Mutex
	peers map[string]*webrtc.PeerConnection // keyed by remote dev
	api   *webrtc.API
}

func NewManager(dev, relayURL, downloadDir string, logger *log.Logger) *Manager {
	return &Manager{
		dev:         dev,
		iceBase:     wsToHTTP(relayURL),
		downloadDir: downloadDir,
		log:         logger,
		peers:       map[string]*webrtc.PeerConnection{},
		api:         webrtc.NewAPI(),
	}
}

func (m *Manager) SetSendSignal(f SendSignalFunc) { m.sendSignal = f }

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

func (m *Manager) track(dev string, pc *webrtc.PeerConnection) {
	m.mu.Lock()
	if old := m.peers[dev]; old != nil {
		_ = old.Close()
	}
	m.peers[dev] = pc
	m.mu.Unlock()
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

// iceServers fetches the relay's ICE config (STUN, + TURN if configured), with a
// public-STUN fallback if the relay is unreachable.
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
		m.mu.Lock()
		pc := m.peers[fromDev]
		m.mu.Unlock()
		if pc != nil {
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sig.SDP}); err != nil {
				m.log.Printf("p2p: set answer: %v", err)
			}
		}
	}
}

// SendFile establishes a connection to toDev and streams path over it. Blocks until
// the receiver acknowledges completion (or it fails / times out).
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
	m.track(toDev, pc)
	defer func() { pc.Close(); m.untrack(toDev) }()

	result := make(chan error, 1)
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
		mime := mimeByExt(path)
		go sendAll(dc, f, head{T: "head", Name: filepath.Base(path), Size: st.Size(), Mime: mime}, m.log)
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	<-webrtc.GatheringCompletePromise(pc)
	m.emit(toDev, Signal{Kind: "offer", SDP: pc.LocalDescription().SDP})
	m.log.Printf("p2p[%s]: offer sent (%s, %d bytes)", toDev, filepath.Base(path), st.Size())

	select {
	case err := <-result:
		if err == nil {
			m.log.Printf("p2p[%s]: transfer complete", toDev)
		}
		time.Sleep(200 * time.Millisecond) // let the final ack flush
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
	m.track(fromDev, pc)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		m.log.Printf("p2p[%s]: %s", fromDev, s)
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			m.untrack(fromDev)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { m.receive(fromDev, dc) })

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
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
	<-webrtc.GatheringCompletePromise(pc)
	m.emit(fromDev, Signal{Kind: "answer", SDP: pc.LocalDescription().SDP})
}

// receive consumes head + chunks + done on dc, writing to the downloads dir.
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
				t, err := os.CreateTemp(m.downloadDir, ".bgn-*.part")
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
				notify("bgnconnect", "Received "+filepath.Base(final))
			}
			return
		}
		// binary chunk
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

func notify(title, body string) {
	_ = exec.Command("notify-send", "-a", "bgnconnect", title, body).Start()
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
