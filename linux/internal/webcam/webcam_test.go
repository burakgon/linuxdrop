package webcam

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu  sync.Mutex
	out [][]byte
}

func (s *fakeSink) Emit(toDev string, payload []byte) error {
	s.mu.Lock()
	s.out = append(s.out, payload)
	s.mu.Unlock()
	return nil
}

func (s *fakeSink) all() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	dup := make([][]byte, len(s.out))
	copy(dup, s.out)
	return dup
}

func TestManager_Start_EmitsRequest(t *testing.T) {
	sink := &fakeSink{}
	m := NewManager(Config{
		Device:        "/dev/null",
		ResolveTarget: func(name string) string { return name },
		Emit:          sink.Emit,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		_ = m.Start(ctx, StartOpts{Target: "phone-dev", Width: 1280, Height: 720, FPS: 30, Camera: "back", Codec: "h264"})
	}()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.all()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := sink.all()
	if len(got) == 0 {
		t.Fatal("no signal emitted within 1s")
	}
	var sig map[string]any
	if err := json.Unmarshal(got[0], &sig); err != nil {
		t.Fatalf("emitted payload not JSON: %v", err)
	}
	if sig["kind"] != "webcam-request" {
		t.Fatalf("first signal kind = %v, want webcam-request", sig["kind"])
	}
	if sig["camera"] != "back" || sig["codec_pref"] != "h264" {
		t.Fatalf("request opts not forwarded: %+v", sig)
	}
	cancel()
}

func TestManager_StartTwiceRejects(t *testing.T) {
	sink := &fakeSink{}
	m := NewManager(Config{
		Device:        "/dev/null",
		ResolveTarget: func(name string) string { return name },
		Emit:          sink.Emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() {
		// Wait briefly so the second Start sees the active session.
		close(started)
		_ = m.Start(ctx, StartOpts{Target: "x", Width: 1280, Height: 720, FPS: 30, Camera: "back", Codec: "h264"})
	}()
	<-started
	// Spin until the manager is in the active state.
	for i := 0; i < 100; i++ {
		if m.Active() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !m.Active() {
		t.Fatal("manager not active after Start")
	}
	if err := m.Start(context.Background(), StartOpts{}); err == nil {
		t.Fatal("second Start should fail")
	}
}

func TestSessionID_8BytesHex(t *testing.T) {
	id := newSessionID()
	if len(id) != 16 {
		t.Fatalf("session id len %d, want 16 (8 bytes hex)", len(id))
	}
	if _, err := hexDecode(id); err != nil {
		t.Fatalf("session id not hex: %v", err)
	}
}

func TestDetectNegotiatedCodec(t *testing.T) {
	cases := map[string]string{
		"m=video 9 UDP/TLS/RTP/SAVPF 100\r\na=rtpmap:100 H265/90000\r\n": "hevc",
		"m=video 9 UDP/TLS/RTP/SAVPF 102\r\na=rtpmap:102 H264/90000\r\n": "h264",
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 VP8/90000\r\n":    "?",
	}
	for in, want := range cases {
		if got := detectNegotiatedCodec(in); got != want {
			t.Errorf("detectNegotiatedCodec(%q) = %q, want %q", in, got, want)
		}
	}
}
