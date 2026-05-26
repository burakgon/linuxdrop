// Package clipboard reads and writes the system clipboard. The Wayland backend
// uses wl-clipboard (wl-paste/wl-copy) over the wlroots/ext data-control
// protocol, which is event-driven — no polling. See proto/PROTOCOL.md §5.
package clipboard

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strings"
)

const imageMime = "image/png" // most apps offer screenshots/copied images as PNG

type Wayland struct {
	mime string
}

func NewWayland() *Wayland { return &Wayland{mime: "text/plain"} }

// Watch blocks, invoking onChange for each new clipboard text until ctx is done.
//
// `wl-paste --watch` runs a command on every change and pipes the new content to
// the command's stdin. We use `sh -c 'cat; printf "\0"'`: cat forwards the full
// content, then a NUL terminator frames it — delimiter-safe for arbitrary,
// multi-line text. The first event reflects the clipboard's current content.
func (w *Wayland) Watch(ctx context.Context, onChange func(text string)) error {
	// Per event: skip when wl-paste marks the selection sensitive (password
	// managers set this); otherwise emit the content + a NUL terminator.
	const script = `if [ "$CLIPBOARD_STATE" = sensitive ]; then cat >/dev/null; else cat; printf '\0'; fi`
	cmd := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", w.mime, "--watch", "sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(stdout)
	for {
		seg, rerr := r.ReadString(0x00)
		if text := strings.TrimSuffix(seg, "\x00"); text != "" {
			onChange(text)
		}
		if rerr != nil {
			break
		}
	}
	return cmd.Wait()
}

// Copy sets the clipboard. wl-copy forks a child to retain ownership while the
// parent returns promptly, so this does not block.
func (w *Wayland) Copy(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", w.mime)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// Paste returns the current clipboard text (empty if none / non-text).
func (w *Wayland) Paste(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", w.mime).Output()
	if err != nil {
		return "", nil // empty or non-text clipboard is not an error for us
	}
	return string(out), nil
}

// WatchImage blocks, invoking onImage with the raw bytes of each new image on the
// clipboard until ctx is done. `wl-paste --watch --type image/png` fires only when
// the selection offers PNG; the child just signals "changed" (a newline), and we
// pull the bytes out-of-band via a fresh wl-paste — NUL/length framing over the
// pipe would be fragile for arbitrary binary data. Sensitive selections are skipped.
func (w *Wayland) WatchImage(ctx context.Context, onImage func(data []byte, mime string)) error {
	const script = `if [ "$CLIPBOARD_STATE" = sensitive ]; then :; else echo x; fi`
	cmd := exec.CommandContext(ctx, "wl-paste", "--type", imageMime, "--watch", "sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(stdout)
	for {
		line, rerr := r.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			if data := w.pasteImage(ctx); len(data) > 0 {
				onImage(data, imageMime)
			}
		}
		if rerr != nil {
			break
		}
	}
	return cmd.Wait()
}

func (w *Wayland) pasteImage(ctx context.Context) []byte {
	out, err := exec.CommandContext(ctx, "wl-paste", "--type", imageMime).Output()
	if err != nil {
		return nil
	}
	return out
}

// CopyImage puts raw image bytes on the clipboard under the given MIME type.
func (w *Wayland) CopyImage(ctx context.Context, data []byte, mime string) error {
	if mime == "" {
		mime = imageMime
	}
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", mime)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}
