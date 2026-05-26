#!/usr/bin/env bash
# Builds bgnconnectd, installs it to ~/.local/bin, and installs the systemd
# user service. Run: bash linux/install.sh
set -euo pipefail
cd "$(dirname "$0")"

BIN_DIR="$HOME/.local/bin"
UNIT_DIR="$HOME/.config/systemd/user"

echo "Building bgnconnectd…"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/bgnconnectd" ./cmd/bgnconnectd

echo "Installing systemd user service…"
mkdir -p "$UNIT_DIR"
cp packaging/bgnconnect.service "$UNIT_DIR/bgnconnect.service"
systemctl --user daemon-reload

echo
echo "Installed: $BIN_DIR/bgnconnectd"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "NOTE: $BIN_DIR is not on PATH — add it to your shell profile." ;;
esac
echo
echo "Next steps:"
echo "  1) Pair (use the QR/URI from your other device, or a hex secret + relay):"
echo "       bgnconnectd pair '<bgnconnect://...>'"
echo "       # or:  bgnconnectd pair <hex-secret> wss://your-relay"
echo "  2) Start on login:"
echo "       systemctl --user enable --now bgnconnect"
echo "  3) Follow logs:"
echo "       journalctl --user -u bgnconnect -f"
echo
echo "Show this device's pairing QR any time:  bgnconnectd qr"
