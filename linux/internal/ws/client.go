// Package ws is a resilient WebSocket client for the relay: it dials, drains an
// outbound queue, sends app-level pings, and reconnects with exponential backoff
// + jitter. It implements engine.Transport.
package ws

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"bgnconnect/linux/internal/wire"
)

const pingInterval = 180 * time.Second // well under the backend's 240s idle timeout

type Client struct {
	url      string
	dev      string
	helloEnc *wire.Enc
	out      chan wire.Envelope
	log      *log.Logger
	onState  func(connected bool)
}

func NewClient(relayURL, roomID, dev string, helloEnc *wire.Enc, logger *log.Logger, onState func(bool)) *Client {
	base := strings.TrimRight(relayURL, "/")
	return &Client{
		url:      base + "/ws?room=" + roomID + "&v=1",
		dev:      dev,
		helloEnc: helloEnc,
		out:      make(chan wire.Envelope, 64),
		log:      logger,
		onState:  onState,
	}
}

// Send queues an envelope for delivery on the current/next connection.
func (c *Client) Send(env wire.Envelope) {
	select {
	case c.out <- env:
	default:
		c.log.Println("ws: outbound queue full, dropping message")
	}
}

// Run manages the connection lifecycle until ctx is cancelled.
func (c *Client) Run(ctx context.Context, onMessage func(wire.Envelope)) {
	const maxBackoff = 60 * time.Second
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		c.connectOnce(ctx, onMessage)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 5*time.Second {
			backoff = time.Second // healthy session → reset backoff
		}
		jitter := time.Duration(rand.Int63n(int64(backoff)/5 + 1)) // up to +20%
		wait := backoff + jitter
		c.log.Printf("ws: reconnecting in %s", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (c *Client) connectOnce(ctx context.Context, onMessage func(wire.Envelope)) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.url, nil)
	cancel()
	if err != nil {
		c.log.Printf("ws: dial failed: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(4 << 20) // 4 MiB

	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()

	c.onState(true)
	defer c.onState(false)
	c.log.Println("ws: connected")

	_ = wsjson.Write(connCtx, conn, wire.Envelope{T: "hello", Dev: c.dev, Ts: wire.Now(), Enc: c.helloEnc})

	// Writer: drains the outbound queue and pings periodically.
	go func() {
		ping := time.NewTicker(pingInterval)
		defer ping.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case env := <-c.out:
				if err := wsjson.Write(connCtx, conn, env); err != nil {
					cancelConn()
					return
				}
			case <-ping.C:
				if err := wsjson.Write(connCtx, conn, wire.Envelope{T: "ping", ID: wire.GenID(), Ts: wire.Now()}); err != nil {
					cancelConn()
					return
				}
			}
		}
	}()

	// Reader.
	for {
		var env wire.Envelope
		if err := wsjson.Read(connCtx, conn, &env); err != nil {
			if connCtx.Err() == nil {
				c.log.Printf("ws: read error: %v", err)
			}
			return
		}
		onMessage(env)
	}
}
