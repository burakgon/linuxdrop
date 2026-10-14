// Package crypto implements LinuxDrop's E2E primitives. These MUST stay
// byte-for-byte compatible with the Kotlin (android) side and the pinned
// vectors in proto/crypto-test-vectors.json. See proto/PROTOCOL.md §4.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

const (
	encSalt    = "linuxdrop/enc/v1"
	encInfo    = "aes-256-gcm"
	tetherSalt = "linuxdrop/tether/v1"
	roomIDLen  = 32
)

// RoomID = base64url(SHA-256(secret)) truncated to the first 32 chars (no padding).
// The backend sees only this; it cannot recover the secret.
func RoomID(secret []byte) string {
	sum := sha256.Sum256(secret)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:roomIDLen]
}

// DeriveKey = HKDF-SHA256(ikm=secret, salt=encSalt, info=encInfo, len=32).
func DeriveKey(secret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, []byte(encSalt), encInfo, 32)
}

// TetherBLEKey = HKDF(secret, salt="linuxdrop/tether/v1", info="ble-aead-key", 32). proto/PROTOCOL.md §8.
func TetherBLEKey(secret []byte) []byte {
	k, _ := hkdf.Key(sha256.New, secret, []byte(tetherSalt), "ble-aead-key", 32)
	return k
}

// TetherSSID = "LD-" + hex(HKDF(secret, …, "softap-ssid", 4)).
func TetherSSID(secret []byte) string {
	b, _ := hkdf.Key(sha256.New, secret, []byte(tetherSalt), "softap-ssid", 4)
	return "LD-" + hex.EncodeToString(b)
}

// TetherPSK = hex(HKDF(secret, …, "softap-psk", 12)).
func TetherPSK(secret []byte) string {
	b, _ := hkdf.Key(sha256.New, secret, []byte(tetherSalt), "softap-psk", 12)
	return hex.EncodeToString(b)
}

// NewCipherWithKey builds a Cipher from a raw 32-byte key (e.g. K_ble), bypassing secret→encKey.
func NewCipherWithKey(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Cipher is an AES-256-GCM box derived from the shared secret.
type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(secret []byte) (*Cipher, error) {
	key, err := DeriveKey(secret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block) // 12-byte nonce, 16-byte tag
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts plaintext under a fresh random nonce and returns base64 strings
// suitable for the wire envelope: iv = base64(nonce), ct = base64(ciphertext||tag).
func (c *Cipher) Seal(plaintext []byte) (iv, ct string, err error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", "", err
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(nonce),
		base64.StdEncoding.EncodeToString(sealed), nil
}

// sealWithNonce is deterministic; used only by tests to match fixed vectors.
func (c *Cipher) sealWithNonce(nonce, plaintext []byte) string {
	return base64.StdEncoding.EncodeToString(c.aead.Seal(nil, nonce, plaintext, nil))
}

// Open decrypts a wire envelope's (iv, ct). A failure (wrong secret / tampering)
// returns an error — this doubles as peer authentication.
func (c *Cipher) Open(ivB64, ctB64 string) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, err
	}
	if len(nonce) != c.aead.NonceSize() {
		return nil, errors.New("crypto: bad nonce size")
	}
	sealed, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, err
	}
	return c.aead.Open(nil, nonce, sealed, nil)
}

// SealBlob encrypts content under a fresh nonce, returning the self-contained blob
// format used for image/file transfer: nonce(12) || ciphertext || tag(16). See
// PROTOCOL.md §6. Must match android's LinuxDropCrypto.sealBlob.
func (c *Cipher) SealBlob(content []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// dst == nonce → output is prefixed with the nonce.
	return c.aead.Seal(nonce, nonce, content, nil), nil
}

// OpenBlob reverses SealBlob: splits the leading nonce, then authenticates+decrypts.
func (c *Cipher) OpenBlob(blob []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("crypto: blob too short")
	}
	return c.aead.Open(nil, blob[:ns], blob[ns:], nil)
}
