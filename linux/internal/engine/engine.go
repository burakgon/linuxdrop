// Package engine wires the clipboard, transport, and cipher together and owns
// the bidirectional sync logic, including loop prevention. See proto/PROTOCOL.md.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bgnconnect/linux/internal/blob"
	"bgnconnect/linux/internal/crypto"
	"bgnconnect/linux/internal/wire"
)

// Clipboard abstracts the OS clipboard (real: clipboard.Wayland; tests: a fake).
type Clipboard interface {
	Watch(ctx context.Context, onChange func(text string)) error
	Copy(ctx context.Context, text string) error
	Paste(ctx context.Context) (string, error)
}

// ImageClipboard is the optional image capability. Backends that implement it
// (clipboard.Wayland) enable image sync; those that don't (test fakes) stay text-only.
type ImageClipboard interface {
	WatchImage(ctx context.Context, onImage func(data []byte, mime string)) error
	CopyImage(ctx context.Context, data []byte, mime string) error
}

// Transport abstracts the relay connection (real: ws.Client; tests: a fake).
type Transport interface {
	Send(env wire.Envelope)
	Run(ctx context.Context, onMessage func(wire.Envelope))
}

type payload struct {
	Type   string `json:"type"` // "text" | "image" | "file"
	Text   string `json:"text,omitempty"`
	Name   string `json:"name,omitempty"`   // blob: original/suggested filename
	Mime   string `json:"mime,omitempty"`   // blob: content type
	Size   int    `json:"size,omitempty"`   // blob: byte length (pre-encryption)
	BlobID string `json:"blobId,omitempty"` // blob: relay store id
	Ch     string `json:"ch"`               // content hash (loop prevention + dedup)
	Origin string `json:"origin"`
	Ts     int64  `json:"ts"`
}

type Engine struct {
	cipher *crypto.Cipher
	clip   Clipboard
	tx     Transport
	blob   *blob.Client // nil → image sync disabled
	room   string
	dev    string
	log    *log.Logger

	mu       sync.Mutex
	lastHash string
	paused   atomic.Bool
	onRoster func(devices []string)
}

// SetPaused toggles syncing without dropping the connection (tray "pause").
func (e *Engine) SetPaused(p bool) { e.paused.Store(p) }

// SetOnRoster registers a callback invoked with the connected device labels.
func (e *Engine) SetOnRoster(f func(devices []string)) { e.onRoster = f }

func New(cipher *crypto.Cipher, clip Clipboard, tx Transport, dev, room string, blobClient *blob.Client, logger *log.Logger) *Engine {
	return &Engine{cipher: cipher, clip: clip, tx: tx, blob: blobClient, room: room, dev: dev, log: logger}
}

func hashText(s string) string { return hashBytes([]byte(s)) }

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) setHash(h string) { e.mu.Lock(); e.lastHash = h; e.mu.Unlock() }
func (e *Engine) sameHash(h string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastHash == h
}

// Run primes the dedup hash from the current clipboard (so we don't broadcast
// stale content on startup), starts the transport, and watches the clipboard.
// It blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if cur, err := e.clip.Paste(ctx); err == nil && cur != "" {
		e.setHash(hashText(cur))
	}
	go e.tx.Run(ctx, e.onInbound)
	if ic, ok := e.clip.(ImageClipboard); ok && e.blob != nil {
		go func() {
			if err := ic.WatchImage(ctx, e.onLocalImage); err != nil && ctx.Err() == nil {
				e.log.Printf("image watch stopped: %v", err)
			}
		}()
	}
	return e.clip.Watch(ctx, e.onLocal)
}

// onLocal handles a local clipboard change: encrypt and send, unless it is an
// echo of content we just received and wrote.
func (e *Engine) onLocal(text string) {
	if text == "" || e.paused.Load() {
		return
	}
	h := hashText(text)
	if e.sameHash(h) {
		return
	}
	e.setHash(h)

	pt, err := json.Marshal(payload{Type: "text", Text: text, Ch: h, Origin: e.dev, Ts: wire.Now()})
	if err != nil {
		return
	}
	iv, ct, err := e.cipher.Seal(pt)
	if err != nil {
		e.log.Printf("encrypt failed: %v", err)
		return
	}
	e.tx.Send(wire.Envelope{
		T: "clip", ID: wire.GenID(), Ts: wire.Now(), Dev: e.dev,
		Enc: &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct},
	})
	e.log.Printf("→ sent clipboard (%d chars)", len([]rune(text)))
}

// onLocalImage handles a local image copied to the clipboard: encrypt, upload as
// a blob, then broadcast a small clip message referencing it. See PROTOCOL.md §6.
func (e *Engine) onLocalImage(data []byte, mime string) {
	if len(data) == 0 || e.paused.Load() || e.blob == nil {
		return
	}
	h := hashBytes(data)
	if e.sameHash(h) {
		return
	}
	e.setHash(h)

	sealed, err := e.cipher.SealBlob(data)
	if err != nil {
		e.log.Printf("image encrypt failed: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id, err := e.blob.Put(ctx, e.room, sealed)
	if err != nil {
		e.log.Printf("image upload failed: %v", err)
		return
	}
	pt, err := json.Marshal(payload{
		Type: "image", Name: "clipboard.png", Mime: mime, Size: len(data),
		BlobID: id, Ch: h, Origin: e.dev, Ts: wire.Now(),
	})
	if err != nil {
		return
	}
	iv, ct, err := e.cipher.Seal(pt)
	if err != nil {
		e.log.Printf("encrypt failed: %v", err)
		return
	}
	e.tx.Send(wire.Envelope{
		T: "clip", ID: wire.GenID(), Ts: wire.Now(), Dev: e.dev,
		Enc: &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct},
	})
	e.log.Printf("→ sent image (%d bytes)", len(data))
}

// onInbound handles a relay message.
func (e *Engine) onInbound(env wire.Envelope) {
	switch env.T {
	case "clip":
		if env.Enc == nil || e.paused.Load() {
			return
		}
		pt, err := e.cipher.Open(env.Enc.IV, env.Enc.Ct)
		if err != nil {
			e.log.Printf("decrypt failed (wrong secret?): %v", err)
			return
		}
		var p payload
		if err := json.Unmarshal(pt, &p); err != nil {
			return
		}
		if p.Origin == e.dev {
			return // our own message bounced back (defense-in-depth)
		}
		switch p.Type {
		case "text":
			e.onInboundText(p)
		case "image", "file":
			e.onInboundBlob(p)
		}
	case "peers":
		e.log.Printf("peers connected: %d", env.Count)
	case "roster":
		e.handleRoster(env)
	}
}

func (e *Engine) onInboundText(p payload) {
	h := hashText(p.Text)
	if e.sameHash(h) {
		return // already have this content
	}
	// Set the hash BEFORE writing so the write-induced watch event is swallowed.
	e.setHash(h)
	if err := e.clip.Copy(context.Background(), p.Text); err != nil {
		e.log.Printf("clipboard write failed: %v", err)
		return
	}
	e.log.Printf("← received clipboard (%d chars)", len([]rune(p.Text)))
}

// onInboundBlob fetches the referenced blob, decrypts it, and writes it to the
// clipboard. Missing/expired blobs are skipped. See PROTOCOL.md §6.
func (e *Engine) onInboundBlob(p payload) {
	ic, ok := e.clip.(ImageClipboard)
	if !ok || e.blob == nil || p.BlobID == "" {
		return
	}
	if e.sameHash(p.Ch) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sealed, err := e.blob.Get(ctx, e.room, p.BlobID)
	if err != nil {
		e.log.Printf("image download skipped: %v", err)
		return
	}
	data, err := e.cipher.OpenBlob(sealed)
	if err != nil {
		e.log.Printf("image decrypt failed: %v", err)
		return
	}
	// Set the hash to the bytes we're about to write so the write-induced watch
	// event is swallowed (loop prevention, mirroring the text path).
	e.setHash(hashBytes(data))
	if err := ic.CopyImage(context.Background(), data, p.Mime); err != nil {
		e.log.Printf("image write failed: %v", err)
		return
	}
	e.log.Printf("← received image (%d bytes)", len(data))
}

func (e *Engine) handleRoster(env wire.Envelope) {
	labels := make([]string, 0, len(env.Devices))
	for _, d := range env.Devices {
		label := d.Dev
		if d.Enc != nil {
			if pt, err := e.cipher.Open(d.Enc.IV, d.Enc.Ct); err == nil {
				var p struct {
					Name     string `json:"name"`
					Platform string `json:"platform"`
				}
				if json.Unmarshal(pt, &p) == nil && p.Name != "" {
					label = p.Name
					if p.Platform != "" {
						label += " · " + p.Platform
					}
				}
			}
		}
		if d.Dev == e.dev {
			label += " (this device)"
		}
		labels = append(labels, label)
	}
	e.log.Printf("roster: %d device(s) [%s]", len(labels), strings.Join(labels, ", "))
	if e.onRoster != nil {
		e.onRoster(labels)
	}
}
