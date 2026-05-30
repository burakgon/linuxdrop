// Package config handles persistent settings (relay URL, device id) and the
// shared secret (stored in the Secret Service / KWallet, with a 0600 file
// fallback). See proto/PROTOCOL.md §4.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	RelayURL   string `json:"relay_url"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

// Dir returns (and creates) $XDG_CONFIG_HOME/linuxdrop.
func Dir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	d := filepath.Join(base, "LinuxDrop")
	return d, os.MkdirAll(d, 0o700)
}

func Load() (*Config, error) {
	d, err := Dir()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	data, err := os.ReadFile(filepath.Join(d, "config.json"))
	if err == nil {
		_ = json.Unmarshal(data, c)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// No built-in relay: RelayURL stays empty until the user pairs / sets one.
	changed := false
	if c.DeviceID == "" {
		c.DeviceID = "linux-" + randHex(3)
		changed = true
	}
	if c.DeviceName == "" {
		c.DeviceName = defaultDeviceName()
		changed = true
	}
	if changed {
		_ = c.Save()
	}
	return c, nil
}

func defaultDeviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "Linux"
}

func (c *Config) Save() error {
	d, err := Dir()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(d, "config.json"), data, 0o600)
}

func GenerateSecret() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func HexSecret(b []byte) string { return hex.EncodeToString(b) }

func DecodeSecretHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) < 16 {
		return nil, errors.New("secret too short (need >= 16 bytes)")
	}
	return b, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
