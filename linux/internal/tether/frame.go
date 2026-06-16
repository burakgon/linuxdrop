// Package tether brings the Linux box online through the paired phone's hotspot
// when it has no internet. See proto/PROTOCOL.md §8 and the design spec.
package tether

import (
	"encoding/binary"
	"errors"

	"linuxdrop/linux/internal/crypto"
)

const (
	OpEnable    = 1
	OpDisable   = 2
	OpKeepAlive = 3
)

// SealCommand: plaintext = sessionNonce(16) || seq(4 BE) || opcode(1), sealed with K_ble.
func SealCommand(key, sessionNonce []byte, seq uint32, opcode byte) ([]byte, error) {
	if len(sessionNonce) != 16 {
		return nil, errors.New("tether: nonce must be 16 bytes")
	}
	c, err := crypto.NewCipherWithKey(key)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, 0, 21)
	pt = append(pt, sessionNonce...)
	pt = binary.BigEndian.AppendUint32(pt, seq)
	pt = append(pt, opcode)
	return c.SealBlob(pt)
}

// SealStatus: plaintext = opcode(1) || result(1).
func SealStatus(key []byte, opcode, result byte) ([]byte, error) {
	c, err := crypto.NewCipherWithKey(key)
	if err != nil {
		return nil, err
	}
	return c.SealBlob([]byte{opcode, result})
}

// OpenStatus returns (opcode, result) or an error.
func OpenStatus(key, frame []byte) (byte, byte, error) {
	c, err := crypto.NewCipherWithKey(key)
	if err != nil {
		return 0, 0, err
	}
	pt, err := c.OpenBlob(frame)
	if err != nil {
		return 0, 0, err
	}
	if len(pt) < 2 {
		return 0, 0, errors.New("tether: short status")
	}
	return pt[0], pt[1], nil
}

// Command is a verified inbound command (Linux is the central, so this is only used in tests).
type Command struct {
	Seq    uint32
	Opcode byte
}

// Verifier mirrors the Android peripheral's replay check (session nonce + strictly increasing seq).
type Verifier struct {
	key     []byte
	nonce   []byte
	lastSeq uint32
}

func NewVerifier(key, sessionNonce []byte) *Verifier { return &Verifier{key: key, nonce: sessionNonce} }

func (v *Verifier) Open(frame []byte) (Command, error) {
	c, err := crypto.NewCipherWithKey(v.key)
	if err != nil {
		return Command{}, err
	}
	pt, err := c.OpenBlob(frame)
	if err != nil {
		return Command{}, err
	}
	if len(pt) != 21 {
		return Command{}, errors.New("tether: bad command length")
	}
	if string(pt[:16]) != string(v.nonce) {
		return Command{}, errors.New("tether: nonce mismatch")
	}
	seq := binary.BigEndian.Uint32(pt[16:20])
	if seq <= v.lastSeq {
		return Command{}, errors.New("tether: replay/stale seq")
	}
	v.lastSeq = seq
	return Command{Seq: seq, Opcode: pt[20]}, nil
}
