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

echo "Installing 'Send with bgnconnect' file-manager action…"
APP_DIR="$HOME/.local/share/applications"
mkdir -p "$APP_DIR"
cat > "$APP_DIR/bgnconnect-send.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Send with bgnconnect
Comment=Send this file directly to a paired device
Exec=$BIN_DIR/bgnconnectd send %f
Icon=document-send
Terminal=false
MimeType=application/octet-stream;
NoDisplay=false
EOF
update-desktop-database "$APP_DIR" 2>/dev/null || true
# → right-click a file → Open With → "Send with bgnconnect", or drop files onto the launcher.

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
