# LinuxDrop macOS client

The macOS menu bar client syncs text clipboard contents with other LinuxDrop devices. It shares the
protocol, encryption, pairing, and sync engine in [`../shared`](../shared); macOS clipboard access
uses the built-in `pbpaste` and `pbcopy` commands.

## Build and run

Requires Go 1.26.3 or later. From this directory:

```bash
go build -o "$HOME/.local/bin/linuxdrop-mac" ./cmd
linuxdrop-mac qr wss://relay.example.com
# Or pair to a network created on another device:
linuxdrop-mac pair 'linuxdrop://pair?s=...&relay=wss%3A%2F%2Frelay.example.com'
linuxdrop-mac run
```

Scan the printed QR with LinuxDrop on Android, or pair with a URI from an existing device. The shared
secret is stored in macOS Keychain when available. The menu bar item offers connection status,
pause/resume, and quit. Clipboard changes are checked every 350 ms. Image sync, direct file transfer,
and phone tether controls are not implemented in this client.
