package engine

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"testing"

	"linuxdrop/linux/internal/crypto"
	"linuxdrop/linux/internal/wire"
)

// fakeClip mimics the OS clipboard, including the crucial hazard: a Copy()
// causes the watcher to fire (as wl-paste --watch would on our own write).
type fakeClip struct {
	mu       sync.Mutex
	content  string
	onChange func(string)
}

func (f *fakeClip) Watch(ctx context.Context, onChange func(string)) error {
	f.mu.Lock()
	f.onChange = onChange
	f.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (f *fakeClip) Copy(_ context.Context, text string) error {
	f.mu.Lock()
	f.content = text
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb(text) // simulate the watcher firing on our own write
	}
	return nil
}

func (f *fakeClip) Paste(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.content, nil
}

type fakeTx struct {
	mu   sync.Mutex
	sent []wire.Envelope
}

func (t *fakeTx) Send(env wire.Envelope) {
	t.mu.Lock()
	t.sent = append(t.sent, env)
	t.mu.Unlock()
}
func (t *fakeTx) Run(context.Context, func(wire.Envelope)) {}
func (t *fakeTx) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sent)
}
func (t *fakeTx) last() wire.Envelope {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sent[len(t.sent)-1]
}

func newTestEngine(t *testing.T, dev string) (*Engine, *fakeClip, *fakeTx, *crypto.Cipher) {
	t.Helper()
	c, err := crypto.NewCipher([]byte("test-secret-please-ignore-32by!!"))
	if err != nil {
		t.Fatal(err)
	}
	clip := &fakeClip{}
	tx := &fakeTx{}
	e := New(c, clip, tx, dev, "test-room", nil, log.New(io.Discard, "", 0))
	clip.onChange = e.onLocal // as if Watch() had been started
	return e, clip, tx, c
}

func makeClip(t *testing.T, c *crypto.Cipher, origin, text string) wire.Envelope {
	t.Helper()
	pt, _ := json.Marshal(payload{Type: "text", Text: text, Ch: hashText(text), Origin: origin, Ts: wire.Now()})
	iv, ct, err := c.Seal(pt)
	if err != nil {
		t.Fatal(err)
	}
	return wire.Envelope{T: "clip", ID: "x", Dev: origin, Enc: &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct}}
}

func TestLocalChangeIsEncryptedAndSent(t *testing.T) {
	e, _, tx, c := newTestEngine(t, "linuxA")
	e.onLocal("hello world")
	if tx.count() != 1 {
		t.Fatalf("want 1 sent, got %d", tx.count())
	}
	env := tx.last()
	if env.T != "clip" || env.Enc == nil {
		t.Fatal("bad envelope")
	}
	pt, err := c.Open(env.Enc.IV, env.Enc.Ct)
	if err != nil {
		t.Fatalf("decrypt own message: %v", err)
	}
	var p payload
	if err := json.Unmarshal(pt, &p); err != nil {
		t.Fatal(err)
	}
	if p.Text != "hello world" || p.Origin != "linuxA" {
		t.Errorf("payload = %+v", p)
	}
}

func TestUnchangedLocalNotResent(t *testing.T) {
	e, _, tx, _ := newTestEngine(t, "linuxA")
	e.onLocal("same")
	e.onLocal("same")
	if tx.count() != 1 {
		t.Errorf("duplicate local content should not resend: got %d", tx.count())
	}
}

func TestInboundClipIsDecryptedAndWritten(t *testing.T) {
	e, clip, _, c := newTestEngine(t, "linuxA")
	e.onInbound(makeClip(t, c, "linuxB", "from peer"))
	if clip.content != "from peer" {
		t.Errorf("clipboard = %q", clip.content)
	}
}

func TestNoEchoLoop(t *testing.T) {
	e, clip, tx, c := newTestEngine(t, "linuxA")
	// Inbound write triggers fakeClip to fire onChange -> onLocal. It MUST NOT resend.
	e.onInbound(makeClip(t, c, "linuxB", "loop test"))
	if clip.content != "loop test" {
		t.Fatalf("not written: %q", clip.content)
	}
	if tx.count() != 0 {
		t.Errorf("echo loop: %d messages sent after inbound write", tx.count())
	}
}

func TestOwnOriginIgnored(t *testing.T) {
	e, clip, _, c := newTestEngine(t, "linuxA")
	e.onInbound(makeClip(t, c, "linuxA", "mine")) // origin == our dev
	if clip.content != "" {
		t.Errorf("own-origin clip should be ignored, clipboard = %q", clip.content)
	}
}

func TestWrongSecretIgnored(t *testing.T) {
	e, clip, _, _ := newTestEngine(t, "linuxA")
	other, _ := crypto.NewCipher([]byte("a-totally-different-secret-key!!"))
	e.onInbound(makeClip(t, other, "linuxB", "secret stuff")) // undecryptable
	if clip.content != "" {
		t.Errorf("undecryptable clip should be ignored, clipboard = %q", clip.content)
	}
}
