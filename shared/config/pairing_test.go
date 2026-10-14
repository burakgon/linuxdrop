package config

import (
	"bytes"
	"testing"
)

func TestPairingRoundTrip(t *testing.T) {
	secret := GenerateSecret()
	relay := "wss://relay.example.com"
	uri := PairingURI(secret, relay)

	gotSecret, gotRelay, err := ParsePairing(uri)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(gotSecret, secret) {
		t.Errorf("secret mismatch")
	}
	if gotRelay != relay {
		t.Errorf("relay = %q, want %q", gotRelay, relay)
	}
}

func TestParsePairingAcceptsHex(t *testing.T) {
	secret := GenerateSecret()
	got, relay, err := ParsePairing(HexSecret(secret))
	if err != nil {
		t.Fatalf("parse hex: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("secret mismatch")
	}
	if relay != "" {
		t.Errorf("hex should carry no relay, got %q", relay)
	}
}

func TestParsePairingRejectsJunk(t *testing.T) {
	if _, _, err := ParsePairing("not a secret"); err == nil {
		t.Error("expected error for junk input")
	}
	if _, _, err := ParsePairing("linuxdrop://pair?relay=wss://x"); err == nil {
		t.Error("expected error for URI without secret")
	}
}
