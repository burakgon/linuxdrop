// Package blob is the HTTP client for the relay's short-lived, E2E-encrypted blob
// store, used to transfer images/files that don't belong in a WS frame. The bytes
// are sealed by the caller (crypto.SealBlob); the relay never decrypts them.
// See proto/PROTOCOL.md §6.
package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBytes = 25 << 20 // mirror backend MAX_BLOB_BYTES

type Client struct {
	base string // http(s) origin, no trailing slash
	http *http.Client
}

func NewClient(relayURL string) *Client {
	return &Client{base: wsToHTTP(relayURL), http: &http.Client{Timeout: 60 * time.Second}}
}

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

// Put uploads encrypted bytes and returns the relay-assigned blob id.
func (c *Client) Put(ctx context.Context, room string, data []byte) (string, error) {
	if len(data) > maxBytes {
		return "", fmt.Errorf("blob too large: %d bytes", len(data))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.base+"/blob?room="+url.QueryEscape(room), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("blob put: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("blob put: empty id")
	}
	return out.ID, nil
}

// Get downloads the encrypted bytes for a blob id (room-scoped). A missing/expired
// blob surfaces as a non-200 error so the caller can skip it.
func (c *Client) Get(ctx context.Context, room, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/blob/"+url.PathEscape(id)+"?room="+url.QueryEscape(room), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob get: status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
}
