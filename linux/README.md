# LinuxDrop — Linux daemon and macOS client

Go. Bidirectionally, **end-to-end encrypted** syncs the Wayland clipboard (`wl-clipboard`)
through the relay. System tray (StatusNotifierItem), systemd user service, secret stored in
KWallet. Event-driven (no polling).

## Build & install
```bash
bash install.sh        # → ~/.local/bin/linuxdropd + systemd user unit
```
Requirements: Go 1.24+, `wl-clipboard`, a compositor with wlr/ext data-control
(KWin/Plasma 6 ✓).

## macOS client

Build on macOS with Go 1.26.3 or later:
```bash
go build -o "$HOME/.local/bin/linuxdrop-mac" ./cmd/linuxdrop-mac
linuxdrop-mac qr wss://relay.example.com
# Or pair to a network created on another device:
linuxdrop-mac pair 'linuxdrop://pair?s=...&relay=wss%3A%2F%2Frelay.example.com'
linuxdrop-mac run
```
Scan the printed QR with LinuxDrop on Android, or pair with a URI from an existing device. The menu bar app syncs text in both directions,
stores the shared secret in Keychain when available, and offers status, pause/resume, and quit.
macOS clipboard access uses the built-in `pbpaste` / `pbcopy` commands and checks for changes every
350 ms. Image sync, direct file transfer, and phone tether controls are not implemented in this
initial macOS client.

## Commands
| Command | Description |
|---------|-------------|
| `linuxdropd gen-secret` | Generate a new random secret (hex) |
| `linuxdropd pair '<linuxdrop://…>'` | Store secret (+relay) from a pairing URI |
| `linuxdropd pair <hex> wss://relay` | Store secret + relay manually |
| `linuxdropd qr` | This device's pairing URI + QR (terminal) |
| `linuxdropd [--relay U] [--dev-secret H] [--no-tray]` | Run (default: with tray) |

## Pair two devices
1. Generate a secret on one device: `linuxdropd gen-secret` (or "Create network" in the Android app).
2. Pair this device to your relay, then show its QR: `linuxdropd pair <hex> wss://relay.yourdomain.com`
   then `linuxdropd qr`; scan it on the other device. The relay URL is embedded in the pairing URI.
3. Start the service: `systemctl --user enable --now linuxdrop`
4. Follow logs: `journalctl --user -u linuxdrop -f`

## Notes
- There is **no built-in relay** — pass your own via the pairing URI or `pair <hex> <wss://…>`.
- The secret is stored in KWallet / Secret Service (fallback: `~/.config/linuxdrop/secret`, 0600).
- Tray menu: status, **Pause/Resume**, **Show pairing QR**, **Quit**.
- Development: `go run ./cmd/linuxdropd --relay ws://localhost:3000 --dev-secret <hex> --no-tray`.
- Tests: `go test ./...` (crypto test vectors + engine loop-prevention + pairing).
- End-to-end smoke (real clipboard + backend, backs up/restores the clipboard):
  `bun ../scripts/e2e-linux.ts`.

Protocol: [`../proto/PROTOCOL.md`](../proto/PROTOCOL.md).
