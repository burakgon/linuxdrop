# bgnconnect — Linux daemon

Go. Bidirectionally, **end-to-end encrypted** syncs the Wayland clipboard (`wl-clipboard`)
through the relay. System tray (StatusNotifierItem), systemd user service, secret stored in
KWallet. Event-driven (no polling).

## Build & install
```bash
bash install.sh        # → ~/.local/bin/bgnconnectd + systemd user unit
```
Requirements: Go 1.24+, `wl-clipboard`, a compositor with wlr/ext data-control
(KWin/Plasma 6 ✓).

## Commands
| Command | Description |
|---------|-------------|
| `bgnconnectd gen-secret` | Generate a new random secret (hex) |
| `bgnconnectd pair '<bgnconnect://…>'` | Store secret (+relay) from a pairing URI |
| `bgnconnectd pair <hex> wss://relay` | Store secret + relay manually |
| `bgnconnectd qr` | This device's pairing URI + QR (terminal) |
| `bgnconnectd [--relay U] [--dev-secret H] [--no-tray]` | Run (default: with tray) |

## Pair two devices
1. Generate a secret on one device: `bgnconnectd gen-secret` (or "Create network" in the Android app).
2. Pair this device to your relay, then show its QR: `bgnconnectd pair <hex> wss://relay.yourdomain.com`
   then `bgnconnectd qr`; scan it on the other device. The relay URL is embedded in the pairing URI.
3. Start the service: `systemctl --user enable --now bgnconnect`
4. Follow logs: `journalctl --user -u bgnconnect -f`

## Notes
- There is **no built-in relay** — pass your own via the pairing URI or `pair <hex> <wss://…>`.
- The secret is stored in KWallet / Secret Service (fallback: `~/.config/bgnconnect/secret`, 0600).
- Tray menu: status, **Pause/Resume**, **Show pairing QR**, **Quit**.
- Development: `go run ./cmd/bgnconnectd --relay ws://localhost:3000 --dev-secret <hex> --no-tray`.
- Tests: `go test ./...` (crypto test vectors + engine loop-prevention + pairing).
- End-to-end smoke (real clipboard + backend, backs up/restores the clipboard):
  `bun ../scripts/e2e-linux.ts`.

Protocol: [`../proto/PROTOCOL.md`](../proto/PROTOCOL.md).
