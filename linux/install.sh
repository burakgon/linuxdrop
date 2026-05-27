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

echo "Installing file-manager actions (right-click + drag-and-drop)…"
APP_DIR="$HOME/.local/share/applications"
SVCMENU_DIR="$HOME/.local/share/kio/servicemenus"
mkdir -p "$APP_DIR" "$SVCMENU_DIR"

# KDE/Dolphin right-click action — appears directly in the context menu for any file.
cat > "$SVCMENU_DIR/bgnconnect-send.desktop" <<EOF
[Desktop Entry]
Type=Service
Name=bgnconnect
Icon=document-send
MimeType=all/allfiles;
Actions=bgnconnectSend;
X-KDE-Priority=TopLevel

[Desktop Action bgnconnectSend]
Name=Send with bgnconnect
Icon=document-send
Exec=$BIN_DIR/bgnconnectd send %F
EOF
chmod +x "$SVCMENU_DIR/bgnconnect-send.desktop"   # Plasma 6 requires servicemenus to be executable

# App launcher — appears in the app menu so it can be pinned to the panel/desktop;
# dropping files onto the pinned launcher sends them.
cat > "$APP_DIR/bgnconnect-send.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Send with bgnconnect
Comment=Send files directly to a paired device
Exec=$BIN_DIR/bgnconnectd send %F
Icon=document-send
Terminal=false
NoDisplay=false
EOF

update-desktop-database "$APP_DIR" 2>/dev/null || true
kbuildsycoca6 2>/dev/null || kbuildsycoca5 2>/dev/null || true   # refresh KDE's menu cache
# → Dolphin: right-click a file → "Send with bgnconnect". Also pin the launcher to the
#   panel/desktop to drag-and-drop files onto it.

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
