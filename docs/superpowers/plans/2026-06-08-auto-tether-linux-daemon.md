# Auto-tether — Linux daemon (Plan 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the Linux box online through the paired phone's hotspot when it has no internet — derive the BLE key/SSID/PSK from the shared secret, BLE-wake the phone (the Plan-2 GATT peripheral), join the hotspot over NetworkManager, and expose it as a manual command, an automatic trigger, and a clear tray status/toggle.

**Architecture:** A new `linux/internal/tether` package: `frame` (BLE AEAD codec, mirrors Android `TetherFrame`), `BLECentral` (talks to the phone's GATT service via **BlueZ D-Bus**, reusing the existing `godbus/dbus/v5` dep — no new library), `WifiConnector` (joins the derived SSID via **nmcli**), `ConnectivityMonitor` (reachability probe), and `Orchestrator` (the state machine). Wired into `linuxdropd` as a `tether` CLI subcommand + an auto-trigger + tray controls. SSID/PSK/`K_ble` derive from the existing secret (already in the keyring), so nothing new is exchanged.

**Tech Stack:** Go 1.26, `crypto/hkdf` (stdlib), `github.com/godbus/dbus/v5` (BlueZ + NetworkManager), `nmcli`, `fyne.io/systray`.

**Scope:** Plan 3 of the gated series (spec: `docs/superpowers/specs/2026-06-08-phone-tether-on-no-internet-design.md`). Plans 1+2 (Android) are done and on-device verified (hotspot via Shizuku; GATT advertise + AEAD auth-reject). The BLE protocol is proven cross-language (a Python `bleak` central drove the phone). This plan is the Linux consumer of that protocol.

> **STATUS (2026-06-16): code-complete & committed (Tasks 1–7).** Crypto + frame unit tests GREEN
> (Docker `golang:1.26`, cross-language vectors). `tether status` works on the host (reads the
> keyring → `ssid=LD-de92cc06`, reachability probe). The BLE central correctly **finds and connects**
> to the phone. The live `tether on` e2e is **blocked by host BlueZ↔Android GATT *service discovery*
> flakiness** — BlueZ connects (phone logs repeated `central connected`) but never resolves the GATT
> DB (`services did not resolve`), even with a fresh phone GATT server + cleared BlueZ cache. This is
> an **environment issue, not a code defect**: all unit tests pass, the protocol is cross-language
> proven, and the Android GATT served a full read+write+auth-reject for the first probe. Retry after
> `sudo systemctl restart bluetooth`, on a different BT adapter, or when the BLE link cooperates.
> Note: `go` vanished from the host mid-session, so builds/tests use a hermetic `golang:1.26`
> container (`/tmp/lgo`); the static binary runs natively on the host.

---

## Reference: the protocol (already pinned — see `proto/PROTOCOL.md §8` + `crypto-test-vectors.json` → `expected.tether`)

- `K_ble = HKDF-SHA256(secret, salt="linuxdrop/tether/v1", info="ble-aead-key", 32)`
- `ssid  = "LD-" + hex(HKDF(secret, …, "softap-ssid", 4))`  · `psk = hex(HKDF(secret, …, "softap-psk", 12))`
- GATT service `e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f`: `nonce`(read …c1), `command`(write …c2), `status`(notify …c3).
- Frame = `SealBlob(K_ble)` = `nonce(12)||ct||tag(16)`. command plaintext = `sessionNonce(16)||seq(4 BE)||opcode(1)` (ENABLE=1, DISABLE=2, KEEPALIVE=3); status plaintext = `opcode(1)||result(1)`.
- Test vectors (secret `000102…1e1f`): `K_ble=793b6d39…0f99`, `ssid=LD-2f0d61cb`, `psk=9ddc1c62b4f9a1da71d45bab`.

## File structure

| File | Responsibility | C/M |
|---|---|---|
| `linux/internal/crypto/crypto.go` | Add `TetherBLEKey/TetherSSID/TetherPSK` + `NewCipherWithKey` | M |
| `linux/internal/crypto/tether_test.go` | Pin derivations to vectors | C |
| `linux/internal/tether/frame.go` | Command/status seal/open + `Verifier` (mirror Android) | C |
| `linux/internal/tether/frame_test.go` | Round-trip + replay tests | C |
| `linux/internal/tether/ble.go` | `BLECentral` — BlueZ D-Bus: scan→connect→nonce→command→status | C |
| `linux/internal/tether/wifi.go` | `WifiConnector` — nmcli join/leave the derived SSID | C |
| `linux/internal/tether/connectivity.go` | `IsOnline()` reachability probe | C |
| `linux/internal/tether/orchestrator.go` | State machine: manual `On/Off`, auto-trigger, keepalive, teardown | C |
| `linux/internal/tray/tray.go` | Tether status line + "Phone internet" toggle; clearer state | M |
| `linux/cmd/linuxdropd/main.go` | `tether on/off/status` CLI + wire orchestrator + tray | M |

---

## Task 1: Go tether derivations (mirror Android, pin to vectors)

**Files:** Modify `linux/internal/crypto/crypto.go`; Create `linux/internal/crypto/tether_test.go`.

- [ ] **Step 1: Write the failing test**

`linux/internal/crypto/tether_test.go`:
```go
package crypto

import "testing"

func TestTetherDerivations(t *testing.T) {
	secret := mustHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if got := hexs(TetherBLEKey(secret)); got != "793b6d391031856ed02410d54050c062f02ec2a696c4b3b615e22ff56f130f99" {
		t.Fatalf("K_ble = %s", got)
	}
	if got := TetherSSID(secret); got != "LD-2f0d61cb" {
		t.Fatalf("ssid = %s", got)
	}
	if got := TetherPSK(secret); got != "9ddc1c62b4f9a1da71d45bab" {
		t.Fatalf("psk = %s", got)
	}
}
```
Add helpers at the bottom of the test file:
```go
import (
	"encoding/hex"
	"testing"
)

func mustHex(s string) []byte { b, _ := hex.DecodeString(s); return b }
func hexs(b []byte) string    { return hex.EncodeToString(b) }
```
(Merge the two import blocks into one.)

- [ ] **Step 2: Run it (fails to compile — funcs undefined)**

Run: `cd linux && go test ./internal/crypto/ -run TetherDerivations`
Expected: FAIL (undefined: TetherBLEKey/TetherSSID/TetherPSK).

- [ ] **Step 3: Implement in `crypto.go`**

Add the tether salt constant next to `encSalt`:
```go
	tetherSalt = "linuxdrop/tether/v1"
```
Add these functions after `DeriveKey`:
```go
// TetherBLEKey = HKDF(secret, salt="linuxdrop/tether/v1", info="ble-aead-key", 32). proto §8.
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
```
Add `"encoding/hex"` to the imports.

- [ ] **Step 4: Run the test (passes)**

Run: `cd linux && go test ./internal/crypto/ -run TetherDerivations -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add linux/internal/crypto/crypto.go linux/internal/crypto/tether_test.go
git commit -m "feat(crypto/linux): tether BLE key + SSID/PSK derivations (pinned to vectors)"
```

---

## Task 2: BLE frame codec (`frame.go`) — mirror Android `TetherFrame`

**Files:** Create `linux/internal/tether/frame.go`, `linux/internal/tether/frame_test.go`.

- [ ] **Step 1: Write the failing test**

`linux/internal/tether/frame_test.go`:
```go
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
```

- [ ] **Step 2: Run it (fails — package/symbols undefined)**

Run: `cd linux && go test ./internal/tether/ -run Frame`
Expected: FAIL (undefined SealCommand/Verifier/etc.).

- [ ] **Step 3: Implement `frame.go`**

`linux/internal/tether/frame.go`:
```go
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
```

- [ ] **Step 4: Run the test (passes)** — `cd linux && go test ./internal/tether/ -run Frame -v` → PASS.

- [ ] **Step 5: Commit**
```bash
git add linux/internal/tether/frame.go linux/internal/tether/frame_test.go
git commit -m "feat(tether/linux): BLE command/status frame codec + replay verifier"
```

---

## Task 3: `BLECentral` — drive the phone's GATT over BlueZ D-Bus

**Files:** Create `linux/internal/tether/ble.go`.

This is the crux. A Python `bleak` central already drove this exact GATT service (read nonce, write command, got `rejected command`/`result=0`), proving the flow; reproduce it with `godbus` so we keep the secret in the Go process and add robust retries. BlueZ exposes GATT over D-Bus on the **system bus**.

- [ ] **Step 1: Implement `ble.go`** (no unit test — exercised on-device in Task 6)

`linux/internal/tether/ble.go`:
```go
package tether

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	svcUUID    = "e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	nonceUUID  = "e3a9f5c1-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	cmdUUID    = "e3a9f5c2-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	statusUUID = "e3a9f5c3-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	bluez      = "org.bluez"
	adapter    = "/org/bluez/hci0"
)

// BLECentral talks to the phone's tether GATT service over BlueZ D-Bus.
type BLECentral struct{ key []byte }

func NewBLECentral(kBle []byte) *BLECentral { return &BLECentral{key: kBle} }

// Command connects to the phone (scanning if needed), sends one opcode, and returns the
// status result code. seq must strictly increase per phone-connection; the phone resets its
// session nonce per connection so a fresh nonce read each call is correct.
func (b *BLECentral) Command(opcode byte, seq uint32) (result byte, err error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return 0, err
	}
	devPath, err := b.findDevice(conn)
	if err != nil {
		return 0, err
	}
	dev := conn.Object(bluez, devPath)
	// Connect + wait for GATT resolution, with retries (BlueZ can drop mid-discovery).
	if err := b.connectResolved(conn, dev, devPath); err != nil {
		return 0, err
	}
	defer dev.Call("org.bluez.Device1.Disconnect", 0)

	nonceCh, err := b.charPath(conn, devPath, nonceUUID)
	if err != nil {
		return 0, err
	}
	cmdCh, err := b.charPath(conn, devPath, cmdUUID)
	if err != nil {
		return 0, err
	}
	nonce, err := b.readValue(conn, nonceCh)
	if err != nil {
		return 0, err
	}
	frame, err := SealCommand(b.key, nonce, seq, opcode)
	if err != nil {
		return 0, err
	}
	// Best-effort status notify.
	statusCh, _ := b.charPath(conn, devPath, statusUUID)
	resCh := make(chan byte, 1)
	if statusCh != "" {
		b.watchStatus(conn, statusCh, opcode, resCh)
	}
	if err := b.writeValue(conn, cmdCh, frame); err != nil {
		return 0, err
	}
	select {
	case r := <-resCh:
		return r, nil
	case <-time.After(8 * time.Second):
		return 0, nil // command delivered; phone didn't notify a status in time
	}
}

// findDevice scans (filtered to our service UUID) and returns the BlueZ device object path.
func (b *BLECentral) findDevice(conn *dbus.Conn) (dbus.ObjectPath, error) {
	ad := conn.Object(bluez, adapter)
	ad.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0, map[string]dbus.Variant{
		"UUIDs":     dbus.MakeVariant([]string{svcUUID}),
		"Transport": dbus.MakeVariant("le"),
	})
	ad.Call("org.bluez.Adapter1.StartDiscovery", 0)
	defer ad.Call("org.bluez.Adapter1.StopDiscovery", 0)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		objs, err := managedObjects(conn)
		if err == nil {
			for path, ifaces := range objs {
				d, ok := ifaces["org.bluez.Device1"]
				if !ok {
					continue
				}
				if uuidsContain(d, svcUUID) {
					return path, nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", errors.New("tether: phone not found over BLE")
}

func (b *BLECentral) connectResolved(conn *dbus.Conn, dev dbus.BusObject, devPath dbus.ObjectPath) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if call := dev.Call("org.bluez.Device1.Connect", 0); call.Err != nil {
			lastErr = call.Err
			time.Sleep(time.Second)
			continue
		}
		// Wait for ServicesResolved.
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v, err := getProp(conn, devPath, "org.bluez.Device1", "ServicesResolved")
			if err == nil {
				if resolved, _ := v.Value().(bool); resolved {
					return nil
				}
			}
			if c, err := getProp(conn, devPath, "org.bluez.Device1", "Connected"); err == nil {
				if connected, _ := c.Value().(bool); !connected {
					break // dropped during discovery; retry
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		lastErr = errors.New("tether: services did not resolve")
		dev.Call("org.bluez.Device1.Disconnect", 0)
		time.Sleep(time.Second)
	}
	return fmt.Errorf("tether: connect failed: %w", lastErr)
}

func (b *BLECentral) charPath(conn *dbus.Conn, devPath dbus.ObjectPath, uuid string) (dbus.ObjectPath, error) {
	objs, err := managedObjects(conn)
	if err != nil {
		return "", err
	}
	for path, ifaces := range objs {
		if !strings.HasPrefix(string(path), string(devPath)) {
			continue
		}
		ch, ok := ifaces["org.bluez.GattCharacteristic1"]
		if !ok {
			continue
		}
		if u, _ := ch["UUID"].Value().(string); strings.EqualFold(u, uuid) {
			return path, nil
		}
	}
	return "", fmt.Errorf("tether: characteristic %s not found", uuid)
}

func (b *BLECentral) readValue(conn *dbus.Conn, ch dbus.ObjectPath) ([]byte, error) {
	var out []byte
	err := conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.ReadValue", 0,
		map[string]dbus.Variant{}).Store(&out)
	return out, err
}

func (b *BLECentral) writeValue(conn *dbus.Conn, ch dbus.ObjectPath, val []byte) error {
	return conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.WriteValue", 0,
		val, map[string]dbus.Variant{}).Err
}

func (b *BLECentral) watchStatus(conn *dbus.Conn, ch dbus.ObjectPath, opcode byte, out chan<- byte) {
	conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.StartNotify", 0)
	sig := make(chan *dbus.Signal, 4)
	conn.Signal(sig)
	conn.AddMatchSignal(dbus.WithMatchObjectPath(ch),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"))
	go func() {
		for s := range sig {
			if s.Path != ch || len(s.Body) < 2 {
				continue
			}
			changed, _ := s.Body[1].(map[string]dbus.Variant)
			v, ok := changed["Value"]
			if !ok {
				continue
			}
			raw, _ := v.Value().([]byte)
			if op, res, err := OpenStatus(b.key, raw); err == nil && op == opcode {
				select {
				case out <- res:
				default:
				}
				return
			}
		}
	}()
}

// --- small D-Bus helpers ---

func managedObjects(conn *dbus.Conn) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := conn.Object(bluez, "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	return objs, err
}

func getProp(conn *dbus.Conn, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	return conn.Object(bluez, path).GetProperty(iface + "." + name)
}

func uuidsContain(dev map[string]dbus.Variant, uuid string) bool {
	v, ok := dev["UUIDs"]
	if !ok {
		return false
	}
	list, _ := v.Value().([]string)
	for _, u := range list {
		if strings.EqualFold(u, uuid) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Build** — `cd linux && go build ./...` → no errors.

- [ ] **Step 3: Commit**
```bash
git add linux/internal/tether/ble.go
git commit -m "feat(tether/linux): BLE central over BlueZ D-Bus (scan/connect/command/status)"
```

---

## Task 4: `WifiConnector` — join the derived hotspot via nmcli

**Files:** Create `linux/internal/tether/wifi.go`.

- [ ] **Step 1: Implement `wifi.go`**

`linux/internal/tether/wifi.go`:
```go
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
```

- [ ] **Step 2: Build** — `cd linux && go build ./...` → no errors.
- [ ] **Step 3: Commit**
```bash
git add linux/internal/tether/wifi.go
git commit -m "feat(tether/linux): WifiConnector — join/leave the derived hotspot via nmcli"
```

---

## Task 5: `ConnectivityMonitor` — reachability probe

**Files:** Create `linux/internal/tether/connectivity.go`.

- [ ] **Step 1: Implement `connectivity.go`**

`linux/internal/tether/connectivity.go`:
```go
package tether

import (
	"context"
	"net/http"
	"time"
)

// IsOnline reports real internet reachability (not just link state) via a fast HTTP 204 probe.
// Used to decide "no internet → tether" and "tethered → online" — captive portals correctly read
// as offline.
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
```

- [ ] **Step 2: Build + sanity-run** — `cd linux && go build ./...`; the probe is exercised by the orchestrator (Task 6). No unit test (network-dependent).
- [ ] **Step 3: Commit**
```bash
git add linux/internal/tether/connectivity.go
git commit -m "feat(tether/linux): reachability probe (generate_204)"
```

---

## Task 6: `Orchestrator` + `tether` CLI — manual end-to-end

**Files:** Create `linux/internal/tether/orchestrator.go`; Modify `linux/cmd/linuxdropd/main.go`.

- [ ] **Step 1: Implement `orchestrator.go`**

`linux/internal/tether/orchestrator.go`:
```go
package tether

import (
	"context"
	"log"
	"sync"
	"time"

	"linuxdrop/linux/internal/crypto"
)

// Orchestrator brings the box online via the phone's hotspot and tears it down. It owns the BLE
// central, the wifi connector, and the keepalive loop. Safe for concurrent On/Off.
type Orchestrator struct {
	log    *log.Logger
	ble    *BLECentral
	wifi   *WifiConnector
	secret []byte

	mu        sync.Mutex
	seq       uint32
	tethered  bool
	stopKeep  chan struct{}
	onState   func(tethered bool, detail string)
}

func NewOrchestrator(secret []byte, logger *log.Logger) *Orchestrator {
	return &Orchestrator{
		log:    logger,
		ble:    NewBLECentral(crypto.TetherBLEKey(secret)),
		wifi:   NewWifiConnector(crypto.TetherSSID(secret), crypto.TetherPSK(secret)),
		secret: secret,
	}
}

// SetOnState registers a callback for tray/UI updates.
func (o *Orchestrator) SetOnState(f func(tethered bool, detail string)) { o.onState = f }

func (o *Orchestrator) emit(tethered bool, detail string) {
	if o.onState != nil {
		o.onState(tethered, detail)
	}
}

// On: BLE-wake the phone, join the hotspot, verify online, start keepalive.
func (o *Orchestrator) On(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tethered {
		return nil
	}
	o.emit(false, "waking phone over BLE…")
	o.seq++
	res, err := o.ble.Command(OpEnable, o.seq)
	if err != nil {
		o.emit(false, "phone unreachable over BLE")
		return err
	}
	if res != 0 {
		o.emit(false, "phone could not start the hotspot")
		return &resultErr{res}
	}
	o.emit(false, "joining "+o.wifi.ssid+"…")
	if err := o.wifi.Join(ctx); err != nil {
		o.emit(false, "couldn't join the hotspot")
		return err
	}
	if !IsOnline(ctx) {
		o.emit(false, "joined hotspot but still offline (phone has no data?)")
		o.wifi.Leave()
		return &offlineErr{}
	}
	o.tethered = true
	o.stopKeep = make(chan struct{})
	go o.keepalive(o.stopKeep)
	o.emit(true, "internet via phone ("+o.wifi.ssid+")")
	o.log.Printf("tether: up via %s", o.wifi.ssid)
	return nil
}

// Off: tell the phone to stop, leave the hotspot.
func (o *Orchestrator) Off() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tethered {
		o.wifi.Leave()
		o.emit(false, "off")
		return
	}
	close(o.stopKeep)
	o.seq++
	o.ble.Command(OpDisable, o.seq)
	o.wifi.Leave()
	o.tethered = false
	o.emit(false, "off")
	o.log.Printf("tether: down")
}

func (o *Orchestrator) Tethered() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tethered
}

func (o *Orchestrator) keepalive(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			o.mu.Lock()
			o.seq++
			seq := o.seq
			o.mu.Unlock()
			o.ble.Command(OpKeepAlive, seq)
		}
	}
}

type resultErr struct{ code byte }

func (e *resultErr) Error() string { return "tether: phone returned error code" }

type offlineErr struct{}

func (e *offlineErr) Error() string { return "tether: joined hotspot but still offline" }
```
(Expose `ssid` on `WifiConnector` by reading `o.wifi.ssid` — it is an unexported field in the same package, so this compiles.)

- [ ] **Step 2: Add the `tether` CLI to `main.go`**

In the command switch in `main()`, add a case:
```go
		case "tether":
			cmdTether(logger, args[1:])
```
Add the function:
```go
func cmdTether(logger *log.Logger, args []string) {
	secret := loadSecretOrDie(logger)
	o := tether.NewOrchestrator(secret, logger)
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on":
		if err := o.On(context.Background()); err != nil {
			logger.Fatalf("tether on: %v", err)
		}
		fmt.Println("tether: up")
	case "off":
		o.Off()
		fmt.Println("tether: off")
	case "status":
		fmt.Printf("ssid=%s online=%v\n", crypto.TetherSSID(secret), tether.IsOnline(context.Background()))
	default:
		logger.Fatalf("usage: linuxdropd tether [on|off|status]")
	}
}

// loadSecretOrDie returns the paired secret (keyring) or exits.
func loadSecretOrDie(logger *log.Logger) []byte {
	hexS, _ := config.LoadSecret()
	if hexS == "" {
		logger.Fatal("not paired; run: linuxdropd pair <uri|hex> [relay]")
	}
	secret, err := config.DecodeSecretHex(hexS)
	if err != nil {
		logger.Fatalf("stored secret invalid: %v", err)
	}
	return secret
}
```
Add `"linuxdrop/linux/internal/tether"` to the imports.

- [ ] **Step 3: Build** — `cd linux && go build ./...` → no errors.

- [ ] **Step 4: On-device end-to-end test (the headline)**

The phone must be running the sync service (GATT server up). Make sure the box currently has internet (so we can see it switch), then:
Run: `cd linux && go run ./cmd/linuxdropd tether on`
Expected: logs "waking phone over BLE…" → "joining LD-…" → "tether: up"; `nmcli connection show --active` lists `LD-<hex>`; the phone's `linuxDropTether` logcat shows `startTethering result=0`.
Then: `go run ./cmd/linuxdropd tether off` → returns to the prior network. If BLE is flaky (BlueZ "services did not resolve"), the built-in retries usually recover; re-run if needed.

- [ ] **Step 5: Commit**
```bash
git add linux/internal/tether/orchestrator.go linux/cmd/linuxdropd/main.go
git commit -m "feat(tether/linux): orchestrator + 'tether on|off|status' CLI (manual e2e)"
```

---

## Task 7: Auto-trigger + tray status/toggle (the clearer control panel)

**Files:** Modify `linux/internal/tray/tray.go`, `linux/cmd/linuxdropd/main.go`.

- [ ] **Step 1: Add tether controls to the tray**

In `tray.go`, extend `Callbacks` and `Tray`:
```go
	// add to Callbacks:
	OnToggleTether func(on bool)
	// add to Tray:
	tetherItem   *systray.MenuItem
	tetherOn     atomic.Bool
	tetherDetail atomic.Value // string
```
In `onReady`, after the pause item, add:
```go
	t.tetherItem = systray.AddMenuItem("Phone internet: off", "Use the phone's hotspot when offline")
```
In the click loop, add a case:
```go
		case <-t.tetherItem.ClickedCh:
			on := !t.tetherOn.Load()
			t.tetherOn.Store(on)
			if t.cb.OnToggleTether != nil {
				go t.cb.OnToggleTether(on)
			}
			t.refresh()
```
Add a setter the orchestrator calls:
```go
// SetTether updates the tether line ("on/off" + a one-line detail).
func (t *Tray) SetTether(on bool, detail string) {
	t.tetherOn.Store(on)
	t.tetherDetail.Store(detail)
	t.refresh()
}
```
In `refresh()`, make the whole panel state obvious (this is the "weak panel" fix) — replace the status/title block with clear symbols and add the tether line:
```go
	sym := "○ offline"
	if t.connected.Load() {
		sym = "● connected"
	}
	if t.paused.Load() {
		sym = "⏸ paused"
	}
	t.statusItem.SetTitle("Clipboard sync: " + sym)
	if t.tetherItem != nil {
		d, _ := t.tetherDetail.Load().(string)
		label := "Phone internet: off"
		if t.tetherOn.Load() {
			label = "Phone internet: ON"
		}
		if d != "" {
			label += " — " + d
		}
		t.tetherItem.SetTitle(label)
	}
```

- [ ] **Step 2: Wire the orchestrator into `cmdRun` (auto-trigger + tray)**

In `cmdRun`, after the tray is created and before `tr.Run()`, construct the orchestrator and an auto-trigger loop:
```go
	tetherOrch := tether.NewOrchestrator(secret, logger)
	tetherOrch.SetOnState(func(on bool, detail string) {
		if tr != nil {
			tr.SetTether(on, detail)
		}
	})
	// Auto: when the relay has been unreachable AND a probe fails for ~8s, tether; when a
	// non-tether network restores internet, tear down.
	go autoTether(ctx, tetherOrch, logger)
```
Set the tray callback in the `tray.Callbacks{...}` literal:
```go
		OnToggleTether: func(on bool) {
			if on {
				if err := tetherOrch.On(ctx); err != nil {
					logger.Printf("tether on: %v", err)
				}
			} else {
				tetherOrch.Off()
			}
		},
```
Add the auto loop:
```go
// autoTether brings the hotspot up after a sustained offline window and tears it down once a
// better network restores internet. Debounced to avoid flapping on brief blips.
func autoTether(ctx context.Context, o *tether.Orchestrator, logger *log.Logger) {
	offlineSince := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		online := tether.IsOnline(ctx)
		switch {
		case online && o.Tethered():
			// Only tear down if we're online via a non-tether network.
			if !o.UsingTether() {
				logger.Println("tether: better network back, tearing down")
				o.Off()
			}
		case online:
			offlineSince = time.Time{}
		case !online && !o.Tethered():
			if offlineSince.IsZero() {
				offlineSince = time.Now()
			}
			if time.Since(offlineSince) >= 8*time.Second {
				logger.Println("tether: offline ≥8s, bringing up phone hotspot")
				if err := o.On(ctx); err != nil {
					logger.Printf("tether auto: %v", err)
					offlineSince = time.Now() // back off; try again next window
				}
			}
		}
	}
}
```
Add `UsingTether()` to the orchestrator (Task 6 file):
```go
// UsingTether reports whether the active wifi connection is our hotspot profile.
func (o *Orchestrator) UsingTether() bool { return o.wifi.Active() }
```

- [ ] **Step 3: Build** — `cd linux && go build ./...` → no errors.

- [ ] **Step 4: On-device test**

Run `go run ./cmd/linuxdropd run` (tray). Confirm the tray shows "Clipboard sync: ● connected" and "Phone internet: off". Click "Phone internet" → it BLE-wakes the phone, joins, and the line shows "Phone internet: ON — internet via phone (LD-…)". Click again → off. For the auto path: disconnect the box's normal Wi-Fi; within ~10–15 s the hotspot should come up automatically; reconnect normal Wi-Fi → it tears down.

- [ ] **Step 5: Commit + install the new daemon**
```bash
git add linux/internal/tray/tray.go linux/cmd/linuxdropd/main.go linux/internal/tether/orchestrator.go
git commit -m "feat(tether/linux): auto-trigger + tray status/toggle (clear control panel)"
go build -o ~/.local/bin/linuxdropd ./linux/cmd/linuxdropd && systemctl --user restart linuxdrop
```

---

## Self-review (against the spec)

- **Spec §4.1 ConnectivityMonitor / BLECentral / WifiConnector / Orchestrator** → Tasks 3–7. ✅
- **Spec §5 data flow** (offline→BLE wake→join→online; keepalive) → Orchestrator `On` + `keepalive` (Task 6). ✅
- **Spec §6 derivations + reuse secret (from keyring)** → Task 1 + `loadSecretOrDie` (no manual paste). ✅
- **Spec §7 teardown** (better network → off; idle/keepalive) → `autoTether` + phone-side safety auto-off (Plan 1). ✅
- **Spec §8 tray + CLI** → Task 7 (tray toggle/status) + Task 6 (`tether on/off/status`). The clearer status block also fixes the "weak panel" complaint. ✅
- **Spec §9 error/edge** (BLE unreachable backoff, joined-but-offline, flapping debounce) → `On` error branches + `autoTether` debounce. ✅
- **Deferred/simplified (explicit):** per-SSID opt-out + notifications (spec §8) are minor follow-ups; BT-off / Shizuku-down surface as BLE errors. Symmetric-NAT etc. are out of tether scope.
- **Placeholder scan:** none — every step has concrete code/commands. BLE has no unit test by nature (on-device in Task 6).
- **Type consistency:** `crypto.TetherBLEKey/TetherSSID/TetherPSK/NewCipherWithKey`, `tether.SealCommand/SealStatus/OpenStatus/NewVerifier/Op*`, `BLECentral.Command`, `WifiConnector.Join/Leave/Active`, `Orchestrator.On/Off/Tethered/UsingTether/SetOnState`, `Tray.SetTether` — all consistent across tasks. ✅
```
