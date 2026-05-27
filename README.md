<div align="center">

# 📋 bgnconnect — encrypted clipboard sync for Android ↔ Linux

### Copy on one device, paste on the other — automatically, end-to-end encrypted, on a relay **you host yourself**.

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
![Platforms](https://img.shields.io/badge/platforms-Android%2010%2B%20%C2%B7%20Linux%20(Wayland)-success)
![Self-hosted](https://img.shields.io/badge/server-self--hosted-orange)

</div>

bgnconnect keeps the clipboard in sync across your phone and computer in real time. Copy a link,
an OTP, a paragraph, or a screenshot on one device and it's instantly ready to paste on the other —
**no manual sharing, no cloud account, no vendor in the middle.** Everything is **end-to-end
encrypted**, and the relay that connects your devices is **one you host yourself**.

> **You run your own server.** There is no built-in/default relay — your clipboard never transits
> anyone else's infrastructure. Stand one up in a single command (see [Self-hosting](#-self-hosting)).

## ✨ Features

- 🔒 **End-to-end encrypted** — AES-256-GCM with a key derived from a secret only your devices know.
- 🕵️ **Zero-knowledge relay** — the server only sees an opaque room id; it can't read clipboard
  content, device names, or filenames.
- 📝🖼️ **Text *and* images** — sync clipboard text and images (a screenshot on your phone, pasted on your desktop).
- 📂 **Direct P2P file transfer** — send any file device-to-device over WebRTC; the bytes go **straight peer-to-peer** (full LAN speed, or hole-punched across networks), never through the relay.
- 🔋 **Battery-friendly & event-driven** — no polling. On Android it reads the clipboard in the
  background via **Shizuku** (no root); on Linux via `wl-clipboard`.
- 🕘 **Clipboard history** — the last items are kept locally (encrypted); tap to put one back.
- 📷 **QR pairing & multi-device** — share a network by QR; many devices can join the same network.
- 🔁 **Resilient** — auto-reconnects on network changes; survives sleep/roaming.
- 🛡️ **Privacy by default** — content flagged sensitive (OTP fields, password managers) is skipped.

## 🖼️ Screenshots

<div align="center">

| Onboarding | Home | History |
|:---:|:---:|:---:|
| ![Onboarding](docs/screenshots/onboarding.png) | ![Home](docs/screenshots/home.png) | ![History](docs/screenshots/history.png) |

</div>

## 🧭 How it works

```
Android (Kotlin / Shizuku) ──┐                          ┌── Linux (Go / wl-clipboard)
                             ├──►  your relay  ◄─────────┤
   Foreground service + WS   │   room = hash(secret)     │   daemon + tray + WS
                             └──────────────────────────-┘
        Payload is AES-256-GCM encrypted end-to-end; the relay can't read it.
```

A **secret** (32 random bytes) defines a sync network. Its hash becomes the `roomId` the relay
routes by; an HKDF of it becomes the AES key your devices encrypt with. The relay is a thin,
stateless pub/sub that never sees the secret. Full spec: [`proto/PROTOCOL.md`](proto/PROTOCOL.md).

## 🚀 Self-hosting

You need a small box with Docker and a domain (or a Tailscale/WireGuard address). The bundled
compose runs the relay behind **Caddy**, which provisions a Let's Encrypt certificate
automatically — so `wss://` works out of the box.

```bash
git clone https://github.com/burakgon/bgnconnect.git
cd bgnconnect/backend

# Point your domain's DNS at the host, then:
BGN_DOMAIN=relay.yourdomain.com docker compose up -d --build
```

That's it — your relay is live at `wss://relay.yourdomain.com`. Use that URL when you set up the
first device; other devices receive it automatically from the pairing QR.

- **No public domain?** Run it on a Tailscale/WireGuard network and use `ws://<private-ip>:3000`.
- **Already running nginx?** See the advanced path in [`backend/README.md`](backend/README.md)
  (`docker-compose.prod.yml` + [`deploy/nginx-relay.conf.example`](backend/deploy/nginx-relay.conf.example)).
- **Verify it:** `bun scripts/relay-check.ts wss://relay.yourdomain.com`.

## 📱 Client setup

**Linux (KDE/Wayland):**
```bash
cd linux
go build -o bin/bgnconnectd ./cmd/bgnconnectd
# (or: bash install.sh  — installs a tray app + systemd user unit)

# Pair this device to your relay and create/scan a network:
./bin/bgnconnectd pair <bgnconnect://… | hex> wss://relay.yourdomain.com
./bin/bgnconnectd qr          # show a QR for your phone to scan
./bin/bgnconnectd run         # start syncing (system tray)
./bin/bgnconnectd send <file> [device]   # send a file directly (P2P) to a peer
```

**Sending files is wired into both desktops:**
- **Android** — **Share → bgnconnect** from any app (or the send icon next to a device on the home
  screen), then pick the target device. Received files land in **Downloads** and appear in the
  in-app **history** (tap to open).
- **Linux** — the tray menu's **"Send file…"**, or right-click a file → **Open With → bgnconnect**
  (and dropping files onto the launcher), installed by `install.sh`. Received files land in
  `~/Downloads` and the folder opens automatically.

**Android:**
1. Build/sideload the APK (see [`android/README.md`](android/README.md)) and install **Shizuku**.
2. Open the app → finish the guided Shizuku step.
3. Enter your relay URL and **Create network**, or **Scan QR** from another device.

## 🔐 Security model

- The **secret never reaches the relay.** Pairing is offline (QR / `bgnconnect://` link / hex).
- `roomId = base64url(SHA-256(secret))[:32]`; `encKey = HKDF-SHA256(secret, …)`; payloads are
  **AES-256-GCM** with a fresh random IV each time. A wrong secret fails the GCM tag → rejected.
- The relay stores only the last encrypted frame per room (for reconnect catch-up) and short-lived
  **encrypted blobs** for image transfer (room-scoped, ~30 min TTL). It can decrypt none of it.
- Cross-language crypto is pinned by [`proto/crypto-test-vectors.json`](proto/crypto-test-vectors.json).

## 🛠️ Build from source

| Component | Stack | Build |
|-----------|-------|-------|
| `backend/` | Bun + Hono + SQLite | `bun install && bun test` · `docker compose up -d --build` |
| `linux/`   | Go (+ `wl-clipboard`) | `go build ./... && go test ./...` |
| `android/` | Kotlin + Compose + Shizuku | `bash scripts/build-apk.sh` (hermetic Docker build) |

## 📄 License

Copyright 2026 burakgon — licensed under [Apache-2.0](LICENSE). The reflective `IClipboard`
access pattern on Android is inspired by [scrcpy](https://github.com/Genymobile/scrcpy) (Apache-2.0).
