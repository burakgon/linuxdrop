// Package webcam runs a pion/webrtc PeerConnection that receives one video
// track from the phone, depacketizes RTP, pipes the encoded NALU stream into
// ffmpeg, and writes the resulting YUV420 frames to a v4l2loopback device.
//
// Lifecycle: Start emits a webcam-request, awaits webcam-offer, sets remote,
// creates answer, emits webcam-answer, accepts trickle candidates from both
// sides, runs the media plane until Stop or peer disconnect.
package webcam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"linuxdrop/linux/internal/v4l2"
)

// Signal is the wire format on the relay. Mirrors p2p.Signal but with our kinds.
type Signal struct {
	Kind          string `json:"kind"`
	Session       string `json:"session,omitempty"`
	SDP           string `json:"sdp,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex uint16 `json:"sdpMLineIndex,omitempty"`
	// Webcam-request only:
	Width     int    `json:"w,omitempty"`
	Height    int    `json:"h,omitempty"`
	FPS       int    `json:"fps,omitempty"`
	Camera    string `json:"camera,omitempty"`
	CodecPref string `json:"codec_pref,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// EmitFn sends a JSON payload to the named dev. The engine layer wraps it
// in the E2E-sealed `signal` envelope before it reaches the relay.
type EmitFn func(toDev string, payload []byte) error

// Config holds the long-lived manager configuration.
type Config struct {
	Device        string                   // /dev/video20
	HWAccel       string                   // "" = auto-probe at session start
	ResolveTarget func(name string) string // map "OPPO" → "andr-f44f31" using engine roster
	Emit          EmitFn
	Logger        *log.Logger
}

// StartOpts is per-session config from CLI / tray.
type StartOpts struct {
	Target string // device name or id; passed through ResolveTarget
	Width  int
	Height int
	FPS    int
	Camera string // "back" | "front"
	Codec  string // "h264" | "hevc"
}

// Manager owns at most one active Session.
type Manager struct {
	cfg    Config
	logger *log.Logger

	mu   sync.Mutex
	sess *Session
}

// NewManager constructs a Manager. HWAccel is probed once (cheap) if unset.
func NewManager(cfg Config) *Manager {
	if cfg.HWAccel == "" {
		cfg.HWAccel = probeHWAccel()
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Manager{cfg: cfg, logger: cfg.Logger}
}

// HWAccel returns the chosen hardware decode path.
func (m *Manager) HWAccel() string { return m.cfg.HWAccel }

// Device returns the configured v4l2loopback path.
func (m *Manager) Device() string { return m.cfg.Device }

// Active returns true while a session exists (signaling or streaming).
func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess != nil
}

// Streaming returns true only when a session is actively writing decoded
// frames to the v4l2 device. The keepalive uses this (not Active()) so that
// during the signaling phase — when no real frames are flowing yet — the
// keepalive keeps the v4l2loopback device in CAPTURE mode. Without this,
// the device briefly disappears from Chrome's enumeration between tray-click
// and the first incoming track.
func (m *Manager) Streaming() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return false
	}
	return m.sess.streaming.Load()
}

// Start spins up a new session. Returns an error if one is already running.
// Blocks until the session ends (peer stop, ctx cancel, or fatal error).
func (m *Manager) Start(ctx context.Context, opts StartOpts) error {
	m.mu.Lock()
	if m.sess != nil {
		m.mu.Unlock()
		return errors.New("webcam: a session is already active")
	}
	if opts.Codec == "" {
		opts.Codec = "h264"
	}
	s := &Session{
		id:       newSessionID(),
		manager:  m,
		opts:     opts,
		ctx:      ctx,
		pending:  nil,
		done:     make(chan struct{}),
		startErr: make(chan error, 1),
	}
	m.sess = s
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.sess = nil
		m.mu.Unlock()
	}()
	return s.run()
}

// Stop tears down any active session. Safe to call when idle.
func (m *Manager) Stop(reason string) {
	m.mu.Lock()
	s := m.sess
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.stop(reason)
}

// OnSignal is the dispatch entry point for webcam-* signal kinds. Caller is
// responsible for ensuring the kind is one of webcam-{offer,answer,candidate,stop}.
func (m *Manager) OnSignal(fromDev string, payload []byte) {
	var sig Signal
	if err := json.Unmarshal(payload, &sig); err != nil {
		m.logger.Printf("webcam: bad signal from %s: %v", fromDev, err)
		return
	}
	m.mu.Lock()
	s := m.sess
	m.mu.Unlock()
	if s == nil {
		m.logger.Printf("webcam: %q from %s ignored (no active session)", sig.Kind, fromDev)
		return
	}
	if s.id != sig.Session {
		m.logger.Printf("webcam: session id mismatch %q vs active %q", sig.Session, s.id)
		return
	}
	s.handleSignal(fromDev, sig)
}

// Session is one webcam streaming session.
type Session struct {
	id      string
	manager *Manager
	opts    StartOpts
	ctx     context.Context

	pc        *webrtc.PeerConnection
	writer    *v4l2.Writer
	pipe      *Pipe
	codecUsed string
	streaming atomic.Bool // true while handleTrack is actively writing v4l2 frames

	mu        sync.Mutex
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	done      chan struct{}
	startErr  chan error
	stopOnce  sync.Once
	stopReason string
}

// ID returns the session identifier (for logging).
func (s *Session) ID() string { return s.id }

func (s *Session) emit(sig Signal) {
	sig.Session = s.id
	b, _ := json.Marshal(sig)
	target := s.manager.cfg.ResolveTarget(s.opts.Target)
	if err := s.manager.cfg.Emit(target, b); err != nil {
		s.manager.logger.Printf("webcam: emit %s: %v", sig.Kind, err)
	}
}

func (s *Session) run() error {
	// 1. Send the request — phone will respond with an offer or a stop.
	s.emit(Signal{
		Kind:      "webcam-request",
		Width:     s.opts.Width,
		Height:    s.opts.Height,
		FPS:       s.opts.FPS,
		Camera:    s.opts.Camera,
		CodecPref: s.opts.Codec,
	})
	s.manager.logger.Printf("webcam[%s]: request sent (%dx%d@%d, camera=%s, codec_pref=%s, target=%s, hwaccel=%s)",
		s.id, s.opts.Width, s.opts.Height, s.opts.FPS, s.opts.Camera, s.opts.Codec, s.opts.Target, s.manager.HWAccel())
	// Phones can briefly miss a single request (backgrounded, doze, network burp,
	// WS suspended). Retry every 2s up to 4 times — one usually wins, and the
	// Android supersede handler ignores duplicates safely (same session id).
	retryTicker := time.NewTicker(2 * time.Second)
	defer retryTicker.Stop()
	const maxRetries = 4
	retries := 0

	for {
		select {
		case <-s.ctx.Done():
			s.stop("ctx-cancel")
			return nil
		case <-retryTicker.C:
			s.mu.Lock()
			gotOffer := s.pc != nil
			s.mu.Unlock()
			if gotOffer {
				retryTicker.Stop()
				select {
				case <-s.ctx.Done():
					s.stop("ctx-cancel")
				case <-s.done:
				}
				return nil
			}
			if retries >= maxRetries {
				s.manager.logger.Printf("webcam[%s]: phone unresponsive after %d attempts", s.id, retries+1)
				s.stop("no-response")
				return nil
			}
			retries++
			s.manager.logger.Printf("webcam[%s]: retry %d/%d", s.id, retries, maxRetries)
			s.emit(Signal{
				Kind:      "webcam-request",
				Width:     s.opts.Width,
				Height:    s.opts.Height,
				FPS:       s.opts.FPS,
				Camera:    s.opts.Camera,
				CodecPref: s.opts.Codec,
			})
		case <-s.done:
			return nil
		}
	}
}

func (s *Session) handleSignal(fromDev string, sig Signal) {
	switch sig.Kind {
	case "webcam-offer":
		if err := s.acceptOffer(sig.SDP); err != nil {
			s.manager.logger.Printf("webcam[%s]: acceptOffer: %v", s.id, err)
			s.stop("accept-offer-failed")
		}
	case "webcam-candidate":
		mid := sig.SDPMid
		mline := sig.SDPMLineIndex
		s.addCandidate(webrtc.ICECandidateInit{
			Candidate:     sig.Candidate,
			SDPMid:        &mid,
			SDPMLineIndex: &mline,
		})
	case "webcam-stop":
		s.manager.logger.Printf("webcam[%s]: peer requested stop: %s", s.id, sig.Reason)
		s.stop("peer-stop")
	}
}

func (s *Session) acceptOffer(sdp string) error {
	me := &webrtc.MediaEngine{}
	if err := registerVideoCodecs(me); err != nil {
		return fmt.Errorf("register codecs: %w", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return fmt.Errorf("NewPeerConnection: %w", err)
	}
	s.pc = pc

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		j := c.ToJSON()
		mid := ""
		if j.SDPMid != nil {
			mid = *j.SDPMid
		}
		var mline uint16
		if j.SDPMLineIndex != nil {
			mline = *j.SDPMLineIndex
		}
		s.emit(Signal{
			Kind:          "webcam-candidate",
			Candidate:     j.Candidate,
			SDPMid:        mid,
			SDPMLineIndex: mline,
		})
	})

	pc.OnTrack(s.handleTrack)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.manager.logger.Printf("webcam[%s]: pc state = %s", s.id, state)
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.stop("pc-" + state.String())
		case webrtc.PeerConnectionStateDisconnected:
			go func() {
				time.Sleep(10 * time.Second)
				if pc.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
					s.stop("disconnected-timeout")
				}
			}()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: sdp,
	}); err != nil {
		return fmt.Errorf("SetRemoteDescription: %w", err)
	}
	s.mu.Lock()
	s.remoteSet = true
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, c := range pending {
		_ = pc.AddICECandidate(c)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("CreateAnswer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("SetLocalDescription: %w", err)
	}
	s.codecUsed = detectNegotiatedCodec(answer.SDP)
	s.manager.logger.Printf("webcam[%s]: answering with codec=%s", s.id, s.codecUsed)
	s.emit(Signal{Kind: "webcam-answer", SDP: answer.SDP})
	return nil
}

func (s *Session) addCandidate(c webrtc.ICECandidateInit) {
	s.mu.Lock()
	if !s.remoteSet {
		s.pending = append(s.pending, c)
		s.mu.Unlock()
		return
	}
	pc := s.pc
	s.mu.Unlock()
	if pc != nil {
		_ = pc.AddICECandidate(c)
	}
}

// handleTrack runs the media plane: read RTP → depacketize → ffmpeg → v4l2.
func (s *Session) handleTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	codecMime := track.Codec().MimeType
	codecLabel := "h264"
	var depack rtp.Depacketizer = &codecs.H264Packet{}
	if codecMime == webrtc.MimeTypeH265 {
		codecLabel = "hevc"
		depack = &codecs.H265Packet{}
	}
	s.manager.logger.Printf("webcam[%s]: incoming track codec=%s", s.id, codecMime)

	pipe, err := NewPipe(s.manager.cfg.HWAccel, codecLabel, s.opts.Width, s.opts.Height)
	if err != nil {
		s.manager.logger.Printf("webcam[%s]: NewPipe: %v", s.id, err)
		s.stop("ffmpeg-failed")
		return
	}
	s.pipe = pipe
	defer pipe.Close()
	s.manager.logger.Printf("webcam[%s]: ffmpeg up (hwaccel=%s)", s.id, pipe.HWAccel())

	w, err := v4l2.Open(s.manager.cfg.Device, s.opts.Width, s.opts.Height, s.opts.FPS)
	if err != nil {
		s.manager.logger.Printf("webcam[%s]: v4l2.Open(%s): %v", s.id, s.manager.cfg.Device, err)
		s.stop("v4l2-failed")
		return
	}
	s.writer = w
	defer w.Close()
	s.streaming.Store(true)
	defer s.streaming.Store(false)
	s.manager.logger.Printf("webcam[%s]: streaming to %s", s.id, s.manager.cfg.Device)

	// Reader goroutine: pull YUV from ffmpeg → write to v4l2.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		frames := 0
		for {
			frame, err := pipe.ReadFrame()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.manager.logger.Printf("webcam[%s]: ReadFrame: %v", s.id, err)
				}
				return
			}
			if werr := w.Write(frame); werr != nil {
				s.manager.logger.Printf("webcam[%s]: v4l2 Write: %v", s.id, werr)
				return
			}
			frames++
			if frames == 1 || frames%150 == 0 {
				s.manager.logger.Printf("webcam[%s]: %d frames written", s.id, frames)
			}
		}
	}()

	// Main loop: read RTP, depacketize, write to ffmpeg with Annex-B start codes.
	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			s.manager.logger.Printf("webcam[%s]: ReadRTP: %v", s.id, err)
			break
		}
		nalu, err := depack.Unmarshal(pkt.Payload)
		if err != nil || len(nalu) == 0 {
			continue
		}
		if _, err := pipe.WriteNAL(startCode); err != nil {
			break
		}
		if _, err := pipe.WriteNAL(nalu); err != nil {
			break
		}
	}
	_ = pipe.CloseStdin()
	<-readerDone
}

func (s *Session) stop(reason string) {
	s.stopOnce.Do(func() {
		s.stopReason = reason
		s.emit(Signal{Kind: "webcam-stop", Reason: reason})
		if s.pc != nil {
			_ = s.pc.Close()
		}
		close(s.done)
	})
}

// --- helpers ---

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

// registerVideoCodecs advertises H.264 and H.265 to the SDP negotiator.
// H.264 first → it wins ties because it's the universal floor; H.265 is
// opportunistic and picked when both sides advertise it.
func registerVideoCodecs(me *webrtc.MediaEngine) error {
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeH265,
			ClockRate: 90000,
		},
		PayloadType: 100,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	return nil
}

// detectNegotiatedCodec scans an SDP for which video codec survived negotiation.
func detectNegotiatedCodec(sdp string) string {
	s := strings.ToLower(sdp)
	if strings.Contains(s, "h265") {
		return "hevc"
	}
	if strings.Contains(s, "h264") {
		return "h264"
	}
	return "?"
}
