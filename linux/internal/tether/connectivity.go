package tether

import (
	"context"
	"net/http"
	"time"
)

// IsOnline reports real internet reachability (not just link state) via a fast HTTP 204 probe, so
// captive portals / dead APs correctly read as offline. Used to decide "no internet → tether" and
// "tethered → online".
func IsOnline(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://connectivitycheck.gstatic.com/generate_204", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}
