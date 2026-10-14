package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// PairingURI builds the QR/text pairing string:
//
//	linuxdrop://pair?s=<base64url(secret)>&relay=<relay-url>
func PairingURI(secret []byte, relay string) string {
	s := base64.RawURLEncoding.EncodeToString(secret)
	return fmt.Sprintf("linuxdrop://pair?s=%s&relay=%s", s, url.QueryEscape(relay))
}

// ParsePairing accepts a linuxdrop:// pairing URI or a raw hex secret. It
// returns the secret bytes and, for URIs, the embedded relay URL ("" otherwise).
func ParsePairing(input string) (secret []byte, relay string, err error) {
	text := strings.TrimSpace(input)
	if strings.HasPrefix(text, "linuxdrop://") {
		u, err := url.Parse(text)
		if err != nil {
			return nil, "", err
		}
		s := u.Query().Get("s")
		if s == "" {
			return nil, "", errors.New("pairing URI missing 's'")
		}
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil, "", err
		}
		if len(b) < 16 {
			return nil, "", errors.New("secret too short")
		}
		return b, u.Query().Get("relay"), nil
	}
	b, err := DecodeSecretHex(text)
	return b, "", err
}
