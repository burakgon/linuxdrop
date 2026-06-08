# Design: Auto-tether — bring the laptop online through the phone when it has no internet

**Status:** Approved design, pre-implementation
**Date:** 2026-06-08
**Component(s):** `linux/` (Go daemon), `android/` (Kotlin app), `proto/`

## 1. Problem & goal

When the Linux machine has **no internet** but the paired Android phone has mobile data, the
laptop should automatically gain internet **through the phone's Wi-Fi hotspot** — with **zero user
interaction** (seamless). The phone enables its hotspot on command; the laptop joins it; normal
LinuxDrop (clipboard sync, file transfer over the relay) resumes over the phone's data.

### The core constraint (chicken-and-egg)

The laptop has no internet, so it **cannot ask the phone to enable the hotspot over the relay** —
the relay requires internet. The trigger channel therefore **cannot be the internet**; it must be a
**local radio**. Wi-Fi/mDNS is unavailable (there is no shared network yet — that is exactly what we
are trying to create). The chosen local channel is **BLE** (Bluetooth Low Energy).

### Goals

- Laptop detects "no internet" and, fully automatically, ends up online via the phone's hotspot.
- No OS-level Bluetooth pairing dialog (seamless): authentication is app-layer, using the existing
  LinuxDrop secret.
- Battery-conscious on both ends.
- A safety brake: tray toggle, per-network opt-out, CLI off — and a phone-side auto-off so the
  hotspot can never be stranded "on".

### Non-goals (YAGNI)

- **Not** auto-enabling the phone's mobile *data* if the user turned it off — we detect "hotspot up
  but no upstream" and inform; we do not flip mobile data (separate privilege + data-cost decision).
- **Not** auto-bypassing carrier tethering entitlement by default (policy decision; possible later
  via `WRITE_SECURE_SETTINGS`).
- **Not** Bluetooth-PAN transport — Wi-Fi hotspot only (full bandwidth).
- **Not** NFC/USB triggers.

## 2. Locked decisions

| Axis | Decision |
|---|---|
| Trigger automation | **Silent automatic** on internet loss, with kill switches (tray toggle, per-SSID opt-out, CLI off). |
| Internet transport | **Wi-Fi hotspot** (phone SoftAp → laptop joins). |
| Local signaling | **BLE GATT**, unbonded, app-layer AEAD auth using the LinuxDrop secret. |
| Android hotspot mechanism | Shizuku UserService (shell uid) **binder reflection** → `WifiManager.setSoftApConfiguration` + `TetheringManager.startTethering(TETHERING_WIFI)`. **Not** `cmd wifi start-softap` (root-only). |
| SSID / PSK / BLE key | All derived from the existing secret via HKDF (offline, both sides). |

### Default parameters (configurable)

- Phone-side hotspot **safety auto-off**: 3 minutes with zero connected stations *or* no BLE
  keepalive.
- Laptop **OFFLINE debounce**: 8 seconds of failed reachability probes before declaring offline.
- SoftAp SSID: **visible** (faster NetworkManager discovery), **WPA2** (WPA3 if both support it),
  PSK derived from the secret.
- **Better-network stability** before dropping the tether profile: 10 seconds (anti-thrash).
- **BLE keepalive** interval (laptop → phone, while tethered): 30 seconds.

## 3. Feasibility findings (verified, 2026-06-08)

The whole feature hinges on a shell-uid Shizuku process being able to enable the Wi-Fi hotspot
programmatically. Verified against current AOSP `main`:

- ❌ **`cmd wifi start-softap` is root-only.** `WifiShellCommand` rejects any UID that is not
  `Process.ROOT_UID`; `start-softap`/`stop-softap` are not in its non-privileged allowlist. Shizuku's
  typical wireless-debugging setup yields **shell uid 2000, not root** — so this path does **not**
  work for us.
- ✅ **The binder-reflection path works for shell uid.** Calling the service APIs directly (as
  `ClipboardUserService` already does for `IClipboard`) bypasses `WifiShellCommand`'s self-imposed
  root gate; only the API's real permission applies. The `com.android.shell` package (uid 2000)
  holds:
  - `TETHER_PRIVILEGED` → `TetheringManager.startTethering(TETHERING_WIFI)` / `stopTethering`.
  - `OVERRIDE_WIFI_CONFIG` → `WifiManager.setSoftApConfiguration(...)` to pin a fixed SSID/PSK.
  - It does **not** hold `NETWORK_SETTINGS`, so `WifiManager.startTetheredHotspot(...)` and
    embedding a custom `SoftApConfiguration` inside `TetheringRequest` (which conditionally escalates
    to `NETWORK_SETTINGS`) are avoided. We pin config separately, then call the simple
    `startTethering(int type, ...)` overload (unconditional `TETHER_PRIVILEGED`).
- ✅ **Existence proof:** shipping Shizuku apps already toggle the Wi-Fi hotspot without root (e.g.
  "delta" hotspot manager, "Ultimate Settings"), confirming the path on real devices.

### Residual risks (same class the project already accepts)

1. **OEM variance** (Samsung One UI, Xiaomi HyperOS, etc.) in the tethering/SoftAp binder surface —
   the same "M3, highest-risk, may need on-device tweaks" reflection risk the clipboard code already
   carries.
2. **Carrier tethering entitlement** — some carriers gate tethering behind a provisioning check;
   `startTethering` may fail silently. Mitigation available later via `WRITE_SECURE_SETTINGS`
   (`tether_dun_required` / entitlement settings); off by default.
3. **Android version API shape** — handled by pinning config via `setSoftApConfiguration` then
   `startTethering(int)`, the stable route across Android 11→16.

**Phase 0 is a feasibility spike that gates everything else** (see §10).

> **✅ VALIDATED on-device (2026-06-08, OnePlus CPH2765, current Android): GREEN.** The hotspot
> enables via the shell-uid path (`startTethering result=0`; SoftAp iface `wlan2` up at
> `10.95.17.101/24`, ssid `LD-spike`). The real gotchas were not OEM *signature* variance but
> **shell package-identity** — call the `IWifiManager` / `ITetheringConnector` binders directly,
> passing `packageName="com.android.shell"` (the high-level managers attach the app's
> `getOpPackageName`, which uid 2000 doesn't own) — and **binder-callback marshalling** (the result
> listener must be a real `IIntResultListener.Stub`, not a `java.lang.reflect.Proxy`, whose
> `asBinder()` is null). See plan Task 1 "GATE RESULT" and commit `3c44e2b`.

### Sources

- WifiShellCommand (AOSP main): https://android.googlesource.com/platform/packages/modules/Wifi/+/refs/heads/main/service/java/com/android/server/wifi/WifiShellCommand.java
- Shell AndroidManifest (holds TETHER_PRIVILEGED + OVERRIDE_WIFI_CONFIG): https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/packages/Shell/AndroidManifest.xml
- TetheringManager (startTethering requires TETHER_PRIVILEGED): https://android.googlesource.com/platform/packages/modules/Connectivity/+/refs/heads/main/Tethering/common/TetheringLib/src/android/net/TetheringManager.java
- Shizuku-powered apps (root-free hotspot toggling): https://github.com/krishna3163/shizuku-apps-root-alternative

## 4. Architecture

```
Linux (Go daemon: linuxdropd)                         Android (Kotlin app)
┌─────────────────────────────┐                       ┌──────────────────────────────┐
│ ConnectivityMonitor         │                       │ SyncForegroundService (exists)│
│   NM D-Bus + reachability    │                       │  └─ TetherGattServer (new)    │
│   probe → Online/Offline      │   BLE GATT (AEAD)     │       BLE peripheral, auth    │
│ Orchestrator (state machine) │◄────────────────────►│       command/status chars    │
│   ├─ BLECentral (BlueZ)      │   "ENABLE"/"DISABLE"  │          │                    │
│   └─ WifiConnector (NM)      │   status notify       │          ▼                    │
│ Tray + CLI hooks             │                       │ TetherUserService (new,       │
└─────────────────────────────┘                       │   Shizuku shell-uid)          │
            │ joins Wi-Fi                              │   reflection: setSoftApConfig │
            ▼                                          │   + startTethering(WIFI)      │
   Phone SoftAp (derived SSID/PSK) ◄───────────────────┘   + safety auto-off timer     │
                                                       └──────────────────────────────┘
```

### 4.1 Linux components — new package `linux/internal/tether/`

- **`ConnectivityMonitor`** — subscribes to NetworkManager state changes over D-Bus (fast reaction)
  and runs an **active reachability probe** (the user's own relay health URL; fall back to a
  `generate_204`-style check). "No internet" is decided by **real reachability**, not link state, so
  dead APs and captive portals count as offline. Emits debounced `Online`/`Offline` events.
- **`BLECentral`** — BLE central via BlueZ (Go: `tinygo.org/x/bluetooth`, or `godbus` directly).
  Scans for the LinuxDrop tether service UUID, connects, performs the AEAD challenge-response,
  writes `ENABLE`/`DISABLE`, subscribes to status notifications, sends keepalive while tethered.
- **`WifiConnector`** — NetworkManager (nmcli or D-Bus) to pre-create and activate the hotspot
  connection profile (SSID/PSK known via derivation), wait until it is the active default route with
  verified internet.
- **`Orchestrator`** — the state machine wiring it together (see §5), with backoff, retries,
  teardown, and kill-switch handling. Depends on the three above only through narrow interfaces so it
  is unit-testable with fakes.
- **Tray + CLI hooks** — status display and controls (see §8).

The trigger integrates with the existing connect/reconnect logic (`internal/ws`, `internal/engine`,
`cmd/linuxdropd/main.go`): the relay WebSocket going down is one input to `ConnectivityMonitor`.

### 4.2 Android components — new package `app/.../tether/`

- **`TetherGattServer`** — a BLE peripheral GATT server hosted **inside the existing
  `SyncForegroundService`** (already a `FOREGROUND_SERVICE_CONNECTED_DEVICE`). Advertises the tether
  service UUID; exposes a `command` (write) and `status` (notify) characteristic, plus a `nonce`
  (read) for replay protection. Verifies the AEAD on each command before acting.
- **`TetherUserService`** — a Shizuku UserService that mirrors `ClipboardUserService`, running as
  shell uid. Via `ServiceManager.getService(...)` + reflection on the device's own `$Stub` interfaces
  it calls `setSoftApConfiguration(SoftApConfiguration)` then `startTethering(TETHERING_WIFI, ...)` /
  `stopTethering(TETHERING_WIFI)`, returning success/error codes. Owns the **safety auto-off timer**.
- **Manifest** — add `BLUETOOTH_ADVERTISE` + `BLUETOOTH_CONNECT` (Android 12+). `CHANGE_WIFI_STATE`
  and the connected-device FGS plumbing already exist.

## 5. Data flow & state machine

Happy path:

1. Laptop on home Wi-Fi; Wi-Fi dies. NM signals a change and/or the relay WS drops.
2. `ConnectivityMonitor` probes the relay health URL → fails for ~8 s → emits **OFFLINE**.
3. `Orchestrator`: start BLE scan for the tether service UUID (**only now**, to save power).
4. Find the phone, connect, read `nonce`, write `AEAD(K_ble, nonce ‖ "ENABLE")`.
5. `TetherGattServer` verifies AEAD + nonce freshness → calls `TetherUserService.enable()` →
   reflection → SoftAp up with derived SSID/PSK → notify `UP`.
6. `WifiConnector` activates the pre-derived NM profile → associates → DHCP → default route.
7. `ConnectivityMonitor` probe succeeds → emits **ONLINE**. Relay WS reconnects. LinuxDrop resumes,
   now over the phone's data. Laptop sends periodic BLE keepalive while tethered.
8. Tray shows "Internet via phone (hotspot)". (Success is otherwise silent.)

State machine (laptop):

```
IDLE ──Offline──► TRIGGERING ──BLE+auth ok──► ENABLING ──notify UP──► JOINING
  ▲                   │ (BLE unreachable, backoff x3)      │ (timeout)     │ (join ok + probe ok)
  │                   ▼                                     ▼               ▼
  └──────────────── GAVE_UP ◄───────────────────────────────────────────  TETHERED
                       │ (re-arm on next NM change)                          │
                       │                            BetterNetwork/User/Suspend/RangeLost
                       └──────────────────────────────────────────────────► TEARDOWN ──► IDLE
```

`JOINING` failure modes: joined the tether SSID but probe still fails (no mobile data) → TEARDOWN +
notify; SoftAp never appeared → TEARDOWN + notify (likely OEM/entitlement).

## 6. Crypto & security (reuse the existing secret)

All derived offline by both sides from the existing 32-byte secret via HKDF-SHA256 with distinct
info labels (consistent with the existing `roomId`/`encKey` derivation):

- `ssid  = "LD-" + base32(HKDF(secret, "tether-ssid"))[:8]`
- `psk   = base32(HKDF(secret, "tether-psk"))[:16]`  (WPA2/WPA3 passphrase)
- `K_ble = HKDF(secret, "tether-ble")`               (BLE AEAD key)

- **BLE auth:** unbonded GATT (no Just-Works pairing dialog → seamless). The laptop reads a random
  `nonce`, sends `AEAD(K_ble, nonce ‖ command ‖ aad)`; the phone verifies the tag and nonce freshness
  (monotonic counter / recently-seen window) → **replay-protected**. A wrong secret fails the tag and
  is ignored. App-layer AEAD is the real security boundary — same "the transport is untrusted, our
  crypto is the boundary" philosophy as the rest of the project.
- **SoftAp:** WPA2/WPA3 with the derived PSK. Anyone with the secret is already a trusted device;
  strangers can neither command the hotspot (AEAD) nor join it (WPA2).
- No new secret to manage — the tether trust boundary is identical to the rest of LinuxDrop.

These derivations and AEAD framing are pinned in `proto/crypto-test-vectors.json` for cross-language
agreement, and documented in a new `proto/PROTOCOL.md` section (service/characteristic UUIDs,
command/status framing, nonce/replay rule, HKDF labels).

## 7. Teardown & battery

The subtlety: once tethered we *are* online, so "online ⇒ tear down" would kill our own link. We
distinguish **online-via-tether-SSID** from **online-via-another-network** (match by SSID/profile).

Teardown triggers: a *better* network returns (real Wi-Fi/Ethernet, probe passes, not the tether
SSID — require ~10 s of stability before fully dropping the tether profile, to avoid thrash); user
disables; suspend/lid-close; phone out of BLE range / hotspot lost.

**Phone-side safety auto-off (belt-and-suspenders):** if the SoftAp has zero connected stations for
the safety window (default 3 min) *or* no BLE keepalive arrives, `TetherUserService` stops tethering
itself — so the hotspot can never be stranded "on" draining data/battery.

Battery posture: laptop scans BLE **only while OFFLINE** and stops once connected or after giving up;
phone advertising piggybacks the existing foreground service (cheap).

## 8. Tray / CLI UX

- **Tray:** a status line ("Internet: phone hotspot" / "No internet — phone unreachable"), a
  "Phone internet: [Auto / Off]" toggle, and a per-SSID "Don't auto-tether on this network" opt-out
  (so it won't fight a mostly-fine office network that occasionally blips).
- **CLI:** `linuxdropd tether status|on|off` and `linuxdropd tether disable-here`.
- **Notifications:** success is silent (seamless); only failures notify, deduplicated and
  low-priority.

## 9. Error & edge-case handling

| Situation | Behavior |
|---|---|
| Phone unreachable over BLE (BT off, out of range, app killed) | Time-boxed scan; ~3 retries with backoff (~30 s); then **stop scanning** + low-priority notification; re-arm on next NM change. No infinite scan. |
| Shizuku not running / unauthorized | GATT error status → "Phone isn't running Shizuku" notification. |
| SoftAp reflection fails on this OEM/version (M3 risk) | Phone returns an error code → notification. De-risked by Phase 0. |
| Mobile data OFF on phone | Hotspot up but no upstream; laptop joins, probe still fails → "joined tether SSID but still offline" → teardown + "Phone has no mobile data". (We do not auto-enable data.) |
| Carrier entitlement blocks tethering | `startTethering` failure callback → phone error → notification. Optional later mitigation via `WRITE_SECURE_SETTINGS`; not default. |
| Captive portal on the dead network | Probe checks real reachability → correctly counts as OFFLINE → triggers tether. |
| Wrong secret / unauthorized BLE peer | AEAD tag fails → phone ignores. Neighbor can neither enable nor leech. |
| Flapping / roaming | Debounce OFFLINE (~8 s) and ONLINE; backoff on repeated triggers. |
| Multiple laptops | All that know the PSK may join; BLE commands accepted from any authenticated peer; safety-off when zero stations. |
| Multiple phones near the laptop | Connect to the first authenticated GATT peer matching the service; remember last-good. |

## 10. Phased build

- **Phase 0 — Feasibility spike (GATE):** on-device, throwaway code calling `setSoftApConfiguration`
  + `startTethering` via Shizuku; confirm the AP comes up with a fixed SSID/PSK on the target
  device(s). **Stop and reassess if it fails.**
- **Phase 1 — Android `TetherUserService`:** enable/disable/status via reflection + safety auto-off,
  behind a debug button.
- **Phase 2 — Android `TetherGattServer`:** BLE peripheral + AEAD auth in the existing FGS, wired to
  Phase 1.
- **Phase 3 — Linux `BLECentral` + `WifiConnector`:** manual `linuxdropd tether on` brings the chain
  up end-to-end.
- **Phase 4 — Linux `ConnectivityMonitor` + `Orchestrator`:** auto trigger + teardown + debounce +
  backoff + safety.
- **Phase 5 — UX & polish:** tray status/toggle, per-SSID opt-out, notifications, `PROTOCOL.md`
  section, crypto vectors, E2E test.

Each phase is independently testable; everything stays behind the manual trigger until Phase 4.

## 11. Testing strategy

- **Linux unit:** `Orchestrator` state machine with fake `ConnectivityMonitor`/`BLECentral`/
  `WifiConnector` interfaces (table-driven, the repo's Go idiom): offline→tether→online,
  teardown-on-better-network, phone-unreachable backoff, joined-but-offline (no data), flapping
  debounce.
- **Crypto vectors:** SSID/PSK/BLE-key derivation + AEAD command vectors added to
  `proto/crypto-test-vectors.json` (cross-language pinning).
- **Android unit:** command parsing + AEAD verify + nonce/replay logic. (Reflection is integration-
  only, exercised on-device.)
- **Phase-0 spike:** the on-device feasibility gate above.
- **E2E (manual/scripted):** kill the laptop's Wi-Fi → assert online-via-hotspot within ~15 s;
  restore Wi-Fi → assert teardown. May extend `scripts/e2e-linux.ts`.
