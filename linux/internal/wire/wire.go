// Package wire defines the on-the-wire message envelope shared by the ws client
// and the engine. See proto/PROTOCOL.md §2.
package wire

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Envelope struct {
	T       string         `json:"t"`             // hello | peers | roster | clip | ack | ping | pong | signal
	ID      string         `json:"id,omitempty"`  // ULID-ish; for ack/dedup
	Ts      int64          `json:"ts,omitempty"`  // sender unix-ms
	Dev     string         `json:"dev,omitempty"` // sender device id (UX/log, not routing)
	Ref     string         `json:"ref,omitempty"` // referenced id (ack/pong)
	To      string         `json:"to,omitempty"`  // recipient device id ("signal" only)
	Count   int            `json:"count,omitempty"`
	Enc     *Enc           `json:"enc,omitempty"`     // "clip"/"signal"; also "hello" (sealed {name,platform})
	Devices []RosterDevice `json:"devices,omitempty"` // "roster"
}

// RosterDevice is one presence entry: device id + sealed {name, platform}.
type RosterDevice struct {
	Dev string `json:"dev"`
	Enc *Enc   `json:"enc,omitempty"`
}

// Enc is the E2E-encrypted payload. ct = base64(ciphertext || 16-byte GCM tag).
type Enc struct {
	V   int    `json:"v"`
	Alg string `json:"alg"`
	IV  string `json:"iv"`
	Ct  string `json:"ct"`
}

func Now() int64 { return time.Now().UnixMilli() }

// GenID returns a short random hex id for messages/devices.
func GenID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
