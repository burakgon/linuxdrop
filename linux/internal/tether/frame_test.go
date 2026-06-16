package tether

import (
	"bytes"
	"encoding/hex"
	"testing"

	"linuxdrop/linux/internal/crypto"
)

func TestFrameRoundTripAndReplay(t *testing.T) {
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	key := crypto.TetherBLEKey(secret)
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	v := NewVerifier(key, nonce)

	f1, _ := SealCommand(key, nonce, 1, OpEnable)
	cmd, err := v.Open(f1)
	if err != nil || cmd.Opcode != OpEnable {
		t.Fatalf("open f1: %v op=%v", err, cmd)
	}
	if _, err := v.Open(f1); err == nil {
		t.Fatal("replay of seq 1 must be rejected")
	}
	f0, _ := SealCommand(key, nonce, 1, OpDisable) // stale seq
	if _, err := v.Open(f0); err == nil {
		t.Fatal("stale seq must be rejected")
	}
	wrong := crypto.TetherBLEKey(bytes.Repeat([]byte{9}, 32))
	fw, _ := SealCommand(wrong, nonce, 2, OpEnable)
	if _, err := v.Open(fw); err == nil {
		t.Fatal("wrong key must be rejected")
	}

	st, _ := SealStatus(key, OpEnable, 0)
	op, res, err := OpenStatus(key, st)
	if err != nil || op != OpEnable || res != 0 {
		t.Fatalf("status: op=%d res=%d err=%v", op, res, err)
	}
}
