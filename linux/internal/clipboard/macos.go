//go:build darwin

// Package clipboard provides a macOS clipboard backend using the system's
// pbpaste and pbcopy tools. macOS does not expose clipboard change events to
// ordinary apps, so the backend polls the pasteboard change count indirectly
// by checking the current text at a short interval.
package clipboard

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

type MacOS struct{}

func NewMacOS() *MacOS { return &MacOS{} }

func (m *MacOS) Paste(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/pbpaste").Output()
	if err != nil {
		return "", nil // an empty or non-text clipboard is not an error
	}
	return string(out), nil
}

func (m *MacOS) Copy(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/pbcopy")
	cmd.Stdin = bytes.NewBufferString(text)
	return cmd.Run()
}

func (m *MacOS) Watch(ctx context.Context, onChange func(text string)) error {
	current, _ := m.Paste(ctx)
	if current != "" {
		onChange(current)
	}
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			text, err := m.Paste(ctx)
			if err == nil && text != "" && text != current {
				current = text
				onChange(text)
			}
		}
	}
}
