package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "LinuxDrop"
	keyringUser    = "shared-secret"
)

// SaveSecret stores the hex secret in the Secret Service (KWallet/gnome-keyring),
// falling back to a 0600 file if no keyring is available (e.g. headless).
func SaveSecret(hexSecret string) error {
	if err := keyring.Set(keyringService, keyringUser, hexSecret); err == nil {
		return nil
	}
	d, err := Dir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "secret"), []byte(hexSecret), 0o600)
}

// LoadSecret returns the stored hex secret, or "" if none is set.
func LoadSecret() (string, error) {
	if s, err := keyring.Get(keyringService, keyringUser); err == nil && s != "" {
		return strings.TrimSpace(s), nil
	}
	d, err := Dir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(d, "secret"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
