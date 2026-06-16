package tether

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// WifiConnector joins/leaves the phone's hotspot (WPA2 PSK) via nmcli. The connection profile is
// named after the SSID so we can find and delete it on teardown.
type WifiConnector struct {
	ssid string
	psk  string
}

func NewWifiConnector(ssid, psk string) *WifiConnector { return &WifiConnector{ssid: ssid, psk: psk} }

// Join associates with the hotspot, rescanning until it appears (the phone just turned it on).
func (w *WifiConnector) Join(ctx context.Context) error {
	deadline := time.Now().Add(25 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		exec.CommandContext(ctx, "nmcli", "device", "wifi", "rescan").Run()
		// `nmcli dev wifi connect <ssid> password <psk>` creates+activates a profile named <ssid>.
		out, err := exec.CommandContext(ctx, "nmcli", "device", "wifi", "connect", w.ssid,
			"password", w.psk).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = &nmError{string(out)}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return lastErr
}

// Leave deactivates and deletes the hotspot profile so NetworkManager won't auto-rejoin it.
func (w *WifiConnector) Leave() {
	exec.Command("nmcli", "connection", "down", w.ssid).Run()
	exec.Command("nmcli", "connection", "delete", w.ssid).Run()
}

// Active reports whether the hotspot profile is the active wifi connection.
func (w *WifiConnector) Active() bool {
	out, err := exec.Command("nmcli", "-t", "-f", "NAME", "connection", "show", "--active").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == w.ssid {
			return true
		}
	}
	return false
}

type nmError struct{ out string }

func (e *nmError) Error() string { return "nmcli: " + strings.TrimSpace(e.out) }
