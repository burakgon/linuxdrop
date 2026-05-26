package crypto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// vectors mirrors proto/crypto-test-vectors.json. Keeping Go in lockstep with
// the generator (and thus with Kotlin) guards against subtle KDF/cipher drift.
type vectors struct {
	Input struct {
		SecretHex     string `json:"secret_hex"`
		PlaintextUTF8 string `json:"plaintext_utf8"`
		IVHex         string `json:"iv_hex"`
	} `json:"input"`
	Expected struct {
		RoomID    string `json:"roomId"`
		EncKeyHex string `json:"encKey_hex"`
		CtBase64  string `json:"ct_base64"`
	} `json:"expected"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	path := filepath.Join("..", "..", "..", "proto", "crypto-test-vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoomIDMatchesVector(t *testing.T) {
	v := loadVectors(t)
	if got := RoomID(mustHex(t, v.Input.SecretHex)); got != v.Expected.RoomID {
		t.Errorf("RoomID = %q, want %q", got, v.Expected.RoomID)
	}
}

func TestDeriveKeyMatchesVector(t *testing.T) {
	v := loadVectors(t)
	key, err := DeriveKey(mustHex(t, v.Input.SecretHex))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(key); got != v.Expected.EncKeyHex {
		t.Errorf("encKey = %s, want %s", got, v.Expected.EncKeyHex)
	}
}

func TestSealMatchesVector(t *testing.T) {
	v := loadVectors(t)
	c, err := NewCipher(mustHex(t, v.Input.SecretHex))
	if err != nil {
		t.Fatal(err)
	}
	got := c.sealWithNonce(mustHex(t, v.Input.IVHex), []byte(v.Input.PlaintextUTF8))
	if got != v.Expected.CtBase64 {
		t.Errorf("ct = %s\nwant %s", got, v.Expected.CtBase64)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	c, err := NewCipher([]byte("a-32-byte-or-any-length-secret!!"))
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte(`{"type":"text","text":"hello world 🌍"}`)
	iv, ct, err := c.Seal(pt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Open(iv, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(pt) {
		t.Errorf("round-trip mismatch: %q", out)
	}
}

func TestSealOpenBlobRoundTrip(t *testing.T) {
	c, err := NewCipher([]byte("a-32-byte-or-any-length-secret!!"))
	if err != nil {
		t.Fatal(err)
	}
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i * 7)
	}
	blob, err := c.SealBlob(content)
	if err != nil {
		t.Fatal(err)
	}
	if want := 12 + len(content) + 16; len(blob) != want { // iv + ct + tag
		t.Errorf("blob len = %d, want %d", len(blob), want)
	}
	got, err := c.OpenBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("blob round-trip mismatch")
	}
	blob[20] ^= 0xff // tamper inside ciphertext
	if _, err := c.OpenBlob(blob); err == nil {
		t.Error("expected auth failure on tampered blob")
	}
}

func TestOpenWrongSecretFails(t *testing.T) {
	a, _ := NewCipher([]byte("secret-A"))
	b, _ := NewCipher([]byte("secret-B"))
	iv, ct, err := a.Seal([]byte("top secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(iv, ct); err == nil {
		t.Error("expected auth failure decrypting with wrong secret, got nil")
	}
}
