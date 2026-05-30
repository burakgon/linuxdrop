# LinuxDrop — phone-as-webcam feature (v0.4 design)

**Status:** approved design (brainstorm phase)
**Owner:** burakgon
**Date:** 2026-05-30

## Goal

Use the Android phone's camera as a **virtual webcam on Linux**, so Linux apps (Zoom, Meet, Chrome, OBS, Cheese) see it as a regular USB camera via **v4l2loopback**. The stream goes **device-to-device over WebRTC** — bytes never touch the relay — reusing LinuxDrop's existing pairing, signaling, and ICE infrastructure. The desktop initiates; the phone auto-accepts silently so there is **zero touch on the phone** between sessions after a one-time setup.

## Non-goals (V1)

- **Audio.** Video-only. The Linux video call app keeps using the laptop's mic. Adding mic forwarding is a clean V1.1.
- Recording, photo capture, zoom/exposure/focus remote control.
- More than one phone serving as a webcam at a time.
- Multiple v4l2loopback devices (one fixed device for V1).
- Browser-to-LinuxDrop interop (purely native↔native).

## Architecture

```
┌──── Linux (linuxdropd) ───────────────┐         ┌──── Android (LinuxDrop app) ────┐
│ Tray ▸ "Use phone camera ▸ <device>"  │         │ FG service                      │
│   │  Settings ▸ Webcam                │         │  (fgsType += "camera")          │
│   ▼                                   │         │   ▲                             │
│ internal/webcam.Session ──────────────┤ ── E2E ─┤── net/WebcamSession             │
│   ├ pion/webrtc/v4 PeerConn            │ signals │   ├ CameraX (back/front)        │
│   ├ HEVC RTP depacketize → NALU stream │ via     │   ├ HW H.265 encoder            │
│   ├ ffmpeg pipe (decode → I420)        │ relay   │   └ webrtc-sdk VideoTrack       │
│   └ internal/v4l2.Writer → /dev/video20│         │                                 │
└────────────────────────────────────────┘         └──────────────────────────────────┘
```

**Two distinct connection types share the same signaling but use separate PeerConnections:**

| | File transfer (existing) | Webcam (new) |
|---|---|---|
| PC lifecycle | one-shot, per file | long-lived, per session |
| Channels | DataChannel `linuxdrop-file` | MediaStreamTrack `video/H265` (or `H264`) |
| Initiator | sender side | Linux side |
| Receiver consent | auto-accept | auto-accept (after one-time setup) |
| Stop | after `done` ack | explicit `webcam-stop` or peer disconnect |

The webcam PeerConnection uses the same `signal{to,kind,enc}` envelope already routed by the relay, so the relay needs **no protocol change** for V1.

## Protocol additions (`proto/PROTOCOL.md` §8)

Five new `signal.enc` `kind` values, all E2E-sealed exactly like the file-transfer signals:

| kind | direction | payload | notes |
|---|---|---|---|
| `webcam-request` | Linux → Phone | `{session, w, h, fps, camera, codec_pref}` | Linux asks the phone to start a session. `camera ∈ {"back","front"}`, `codec_pref ∈ {"hevc","h264"}`. |
| `webcam-offer` | Phone → Linux | `{session, sdp}` | Phone's SDP offer with one video transceiver. |
| `webcam-answer` | Linux → Phone | `{session, sdp}` | Linux's SDP answer. |
| `webcam-candidate` | both | `{session, candidate, sdpMid, sdpMLineIndex}` | Trickle ICE candidates; queued until the remote SDP is applied (same logic as file transfer). |
| `webcam-stop` | either | `{session, reason?}` | Tear down. `reason` is human-readable for logs. |

`session` is a short random hex id minted by the initiator (`8` bytes hex). It lets us run a webcam session and a concurrent file transfer in the same room without crossing wires.

Error signaling: if the phone can't honor a `webcam-request`, it responds with `webcam-stop{reason:"no-permission"|"no-camera"|"in-use"}` instead of an offer; the Linux side surfaces it as a `notify-send`.

## Components

### Android (Kotlin)

**New: `app/src/main/java/com/linuxdrop/app/net/WebcamSession.kt`** — ~250 lines.

- Wraps one `PeerConnection` (separate from `P2pManager`'s file PCs; the existing `P2pManager` is kept untouched).
- Builds a video source from `Camera2Enumerator` + a `SurfaceTextureHelper`-backed `VideoCapturer` (webrtc-sdk's built-in `Camera2Capturer`).
- Adds a single video track to the PC. Encoder factory = `DefaultVideoEncoderFactory(eglBase, true, true)` — HW encode on, H.265 + H.264 advertised; the SDK picks H.265 when available.
- Adapts resolution: `videoCapturer.startCapture(w, h, fps)` from the request.
- Exposes `start(req)`, `stop(reason)`, callbacks `onSessionEnded`, `onError`. Idempotent.

**Modified: `app/src/main/java/com/linuxdrop/app/service/SyncForegroundService.kt`**

- Hold one `WebcamSession?` (null when inactive).
- Route inbound `webcam-*` signals to the session (or to a fresh one on `webcam-request`).
- Update `foregroundServiceType` to `connectedDevice|camera` (manifest + `startForeground` flag) so background camera access is permitted.
- Emit a second, lower-priority notification while a session is active: title "Streaming as webcam to <device>", action "Stop".

**Modified: `app/src/main/AndroidManifest.xml`**

- Add `<uses-permission android:name="android.permission.CAMERA"/>`.
- Add `<uses-feature android:name="android.hardware.camera" android:required="false"/>` (don't restrict Play Store visibility; not on Play Store anyway).
- Change service line to `android:foregroundServiceType="connectedDevice|camera"`.
- Optional permission for Android 14+ background camera: `FOREGROUND_SERVICE_CAMERA`.

**Modified: `app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt`**

- New "Webcam" card:
  - Camera permission status row + "Grant" button (runtime request).
  - Default camera dropdown: Back / Front.
  - Default resolution dropdown: 720p / 1080p.
- Persist in the existing `linuxdrop_secret.xml` prefs (or a new `linuxdrop_webcam.xml` — pick one in implementation).

### Linux (Go)

**New: `linux/internal/webcam/webcam.go`** — ~300 lines.

- `type Session struct { pc *webrtc.PeerConnection; loopback *v4l2.Writer; decoder *ffmpegPipe; ... }`.
- `Start(target, opts) error` — sends `webcam-request`, awaits `webcam-offer`, sets remote, creates answer, sends `webcam-answer`, hooks `OnTrack` for video, spins up the decode pipeline.
- `Stop(reason)` — sends `webcam-stop`, closes PC, terminates the ffmpeg subprocess, releases the v4l2 device.
- `OnTrack` handler: reads RTP packets, calls the webrtc/v4 RTP depacketizer (HEVC or H264 based on negotiated codec), writes the resulting NALU stream to `decoder.stdin`.

**New: `linux/internal/webcam/ffmpeg.go`** — ~160 lines.

- HW-accel probe at first use: tries VAAPI → NVDEC → QSV → SW (see "Codec + media pipeline" for full order + env override).
- Spawns ffmpeg with the chosen `-hwaccel` flag, e.g.:
  `ffmpeg -hwaccel vaapi -hwaccel_device /dev/dri/renderD128 -hwaccel_output_format yuv420p -f hevc -i pipe:0 -f rawvideo pipe:1`
  (`-f h264` for the H.264 case; flags mirror.)
- Reads stdout YUV420 frames (size = `w*h*3/2`) and emits them on a channel.
- Restarts the subprocess on EOF / crash; logs to the daemon's structured log. If the chosen HW path fails to open at session start, falls through to the next candidate before giving up.

**New: `linux/internal/v4l2/loopback.go`** — ~150 lines.

- `Open(devicePath, w, h, fps) (*Writer, error)` — `os.OpenFile(O_WRONLY)`, then `VIDIOC_S_FMT` ioctl: `V4L2_PIX_FMT_YUV420`, `V4L2_FIELD_NONE`. Uses `golang.org/x/sys/unix` for the ioctls.
- `Write(frame []byte)` — simple `unix.Write(fd, frame)`. v4l2loopback handles the queue.
- `Close()` — fd close.

**Modified: `linux/cmd/linuxdropd/main.go`**

- New subcommand `webcam`:
  - `linuxdropd webcam install` — writes `/etc/modules-load.d/linuxdrop-webcam.conf` (`v4l2loopback`) and `/etc/modprobe.d/linuxdrop-webcam.conf` (`options v4l2loopback exclusive_caps=1 video_nr=20 card_label="LinuxDrop Camera"`), then `modprobe v4l2loopback`. Requires root → runs `pkexec`. Verifies `/dev/video20` exists. Reports next steps.
  - `linuxdropd webcam start [--to <dev>] [--resolution 720p|1080p] [--camera back|front]` — handshake helper used by the tray; also a CLI for power users.
  - `linuxdropd webcam stop` — sends `webcam-stop` for the active session.
- Wires `p2pMgr`-style: a single `webcam.Manager` instance owned by `cmdRun` next to the existing `p2pMgr`.

**Modified: `linux/internal/tray/tray.go`**

- New submenu **"Use phone camera"**:
  - When inactive: list of connected devices → click starts a session with that device + default resolution/camera (read from a small Linux-side config in `~/.config/linuxdrop/webcam.json`).
  - When active: "📹 Streaming from <device>" header + items "Switch to front/back camera", "Switch resolution", **"Stop"**.
- Module presence check on tray init: if `/dev/video20` (or configured path) is missing, the submenu shows a single disabled item "Run `linuxdropd webcam install` to enable".

### Relay (TS)

**No change.** The existing `signal` forwarder transports the new `webcam-*` kinds verbatim. The relay still sees only opaque `{enc:"…"}`.

## Codec + media pipeline

- **Phone encode:** prefer H.265 (HEVC). webrtc-sdk's default encoder factory advertises HEVC on Android 12+ when the device's MediaCodec exposes an HEVC encoder (true for Snapdragon 8 Gen 1+, Tensor G2+, Dimensity 8000+ — covers anything sold in the last 3 years, including the target OPPO CPH2765). Falls back to H.264 if the receiver only signals H.264 support.
- **Linux decode: HW by default.** ffmpeg subprocess (no cgo). At session start the daemon probes the box for an HW decoder in this order and picks the first that loads:
  1. **VAAPI** (Intel iGPU / AMD via Mesa) — `/dev/dri/renderD128` present + the `va-api` ffmpeg backend can open it. Flag: `-hwaccel vaapi -hwaccel_device /dev/dri/renderD128 -hwaccel_output_format yuv420p`.
  2. **NVDEC (CUDA)** — `nvidia-smi` exits 0. Flag: `-hwaccel cuda -hwaccel_output_format yuv420p`.
  3. **QSV** (Intel-specific path) — only if VAAPI failed. Flag: `-hwaccel qsv`.
  4. **SW** — last-resort fallback. Logged at WARN; the active-session notification mentions "(SW decode)" so the user knows.
  
  The probe runs once per `linuxdropd` start, caches the choice, and re-probes when the user toggles GPU drivers (best-effort: a `linuxdropd webcam reprobe` subcommand forces it). Probing checks both that the device node exists and that ffmpeg can actually open it — we don't trust file presence alone. The CachyOS/KDE target box almost always lands on VAAPI; the SW path exists for headless installs and weird hardware.
- Override: `LINUXDROP_HWACCEL=vaapi|cuda|qsv|sw` env var pins the choice for debugging.
- **Negotiation:** pion's `MediaEngine` is registered with H.265 first, H.264 second. Browsers won't be in the loop; both endpoints are native. SDP `a=rtpmap:97 H265/90000` is the happy path.
- **Bitrate / adaptive:** WebRTC's built-in congestion control (TWCC + GCC) handles bitrate adaptation. Defaults: 720p30 ≈ 1.5 Mbps target, 1080p30 ≈ 3 Mbps. On LAN both run at near-max quality; on hole-punched/TURN paths the encoder drops bitrate gracefully.

## UX flow

**One-time setup (per machine + per phone pair):**

| Step | Where | Action |
|---|---|---|
| 1 | Linux | `linuxdropd webcam install` (one prompt for sudo via pkexec). |
| 2 | Phone | LinuxDrop → Settings → Webcam → "Grant camera permission". One system dialog, one tap. |

After that, both ends remember the choice forever.

**Each session (seamless, zero phone touch):**

1. Click tray ▸ "Use phone camera ▸ OPPO CPH2765".
2. Linux sends `webcam-request`; phone FG service receives → starts CameraX silently. The phone shows only the Android-mandatory status-bar camera dot. No app needs to be open; screen can be off.
3. WebRTC connects in 1–2 s. Frames start flowing into `/dev/video20`.
4. Zoom/Meet/OBS lists "LinuxDrop Camera" as a video source.
5. Click tray ▸ "Stop". Phone releases the camera, indicator goes away.

**Edge cases:**

- **Phone in another app:** fine — background camera is OK with `foregroundServiceType=camera`.
- **Phone screen off:** fine.
- **Phone in DND / locked:** fine for camera; for clarity the active-session notification stays in the lock-screen shade so the user can stop it without unlocking.
- **Phone CAMERA permission revoked later:** `webcam-stop{reason:"no-permission"}` → Linux toasts a hint.
- **Phone goes out of Wi-Fi range mid-session:** WebRTC's `OnConnectionStateChange` fires `disconnected`; both ends teardown after a 10 s grace period and clean up.
- **Linux v4l2loopback not installed:** the tray submenu is disabled with the install hint; `linuxdropd webcam start` exits non-zero with a clear message.

## Error handling

A short list of guarded states:

| Where | Failure | Behavior |
|---|---|---|
| Phone | CAMERA permission denied | `webcam-stop{reason:"no-permission"}` |
| Phone | Camera in use by another app | `webcam-stop{reason:"in-use"}` |
| Phone | HW encoder absent | Falls back to SW H.264; if also unavailable, `webcam-stop{reason:"no-encoder"}` |
| Linux | `/dev/video20` missing | Refuse to start; surface install hint |
| Linux | ffmpeg not on PATH | Refuse to start; surface "Install `ffmpeg` (apt/dnf/pacman)" hint |
| Both | WebRTC fails to connect (no path) | 30 s timeout → `webcam-stop{reason:"ice-failed"}` |
| Both | Peer drops mid-stream | 10 s teardown |

Notification on Linux while active mirrors the phone's: a `notify-send` on start ("Streaming from OPPO CPH2765") and on stop. The tray title gets a small red dot while active.

## Testing strategy

- **Phone unit:** `WebcamSession` smoke test with a fake video source (no real camera): assert start/stop lifecycle, encoder factory wiring, signal emission shape.
- **Linux unit:** `internal/v4l2/loopback` tests against `/dev/null` and a memfd (format ioctls mocked); `internal/webcam` tests with a fake `RTPReceiver` + a recorded HEVC NALU fixture → assert ffmpeg pipe assembles YUV at the right rate.
- **Linux integration:** two-process test on the same machine — a tiny `linux/cmd/webcam-smoke/` that fakes the phone end and verifies the Linux end writes frames to a tmpfs file pretending to be `/dev/video20`.
- **End-to-end manual (release gate):**
  1. Same Wi-Fi: 720p + 1080p, back + front, app open + screen off, restart phone wireless mid-session.
  2. Cross-network (phone on mobile data, laptop on Wi-Fi): same matrix, STUN only.
  3. Strict NAT (force TURN via the bundled coturn): 720p only, latency + bitrate sanity check.
  4. Use the live camera in Cheese, Chrome (Meet `getUserMedia`), and OBS.
  5. **HW vs SW decode:** confirm VAAPI is the picked path on the dev box (CachyOS + KDE) — `linuxdropd webcam status` (or the active-session notification) must show "HW: vaapi"; force SW with `LINUXDROP_HWACCEL=sw` and verify the SW fallback still works.

## V1.1 ideas (explicitly out of scope for V1)

- Audio (phone mic → PipeWire virtual source on Linux).
- "Auto-start webcam when LinuxDrop daemon detects a video-call app launches" — heuristic, opt-in.
- Battery warning when streaming below a threshold.
- Multiple resolutions / dynamic switch without renegotiate.
- Switch camera mid-session without renegotiate (CameraX swap with track replacement).
- Browser interop (likely H264-only path).

## Open questions resolved here

- **Codec:** H.265 primary, H.264 fallback (per user direction "h265 veya daha iyi").
- **Decode dependency:** ffmpeg subprocess (no cgo, no libav linkage). HW decode (VAAPI / NVDEC / QSV) is the default, auto-detected; SW is only the last-resort fallback.
- **Initiator:** Linux. Phone is seamless (no UI interaction per session).
- **Audio:** out of scope for V1.
- **Resolution:** user-selectable in Settings, default 720p.
- **v4l2loopback setup:** one-time `linuxdropd webcam install` (pkexec).

## Acceptance criteria

- ✅ `linuxdropd webcam install` provisions `/dev/video20` persistently across reboots.
- ✅ Settings → Webcam → Grant flows the CAMERA permission once.
- ✅ Tray "Use phone camera" starts the stream end-to-end; Zoom/Meet/OBS see it.
- ✅ Phone status-bar privacy dot appears; no other UI on phone during session.
- ✅ "Stop" cleanly releases the camera on the phone and stops the v4l2 writes.
- ✅ Linux + Go tests pass; manual matrix above passes on the dev phone (OPPO CPH2765) over the production relay.
- ✅ No regression in existing clipboard sync or file transfer.
