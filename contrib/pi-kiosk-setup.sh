#!/usr/bin/env bash
# pingway kiosk setup for Raspberry Pi OS (Bookworm, 64-bit, desktop).
# Points Chromium in kiosk mode at the pingway kiosk display and starts it
# on boot via a systemd user unit. Works on labwc/wayfire (Wayland) and X11.
#
# Usage:
#   ./pi-kiosk-setup.sh [URL]
# Default URL: http://localhost:8080/kiosk
set -euo pipefail

URL="${1:-http://localhost:8080/kiosk}"

if ! command -v chromium-browser >/dev/null 2>&1 && ! command -v chromium >/dev/null 2>&1; then
  echo "Installing chromium..."
  sudo apt-get update && sudo apt-get install -y chromium-browser || sudo apt-get install -y chromium
fi
CHROMIUM="$(command -v chromium-browser || command -v chromium)"

echo "Disabling screen blanking..."
if command -v raspi-config >/dev/null 2>&1; then
  sudo raspi-config nonint do_blanking 1 || true
fi

echo "Creating systemd user unit..."
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/pingway-kiosk.service <<EOF
[Unit]
Description=pingway kiosk display
After=graphical-session.target
PartOf=graphical-session.target

[Service]
ExecStart=${CHROMIUM} --kiosk --noerrdialogs --disable-infobars \\
  --disable-session-crashed-bubble --incognito \\
  --ozone-platform-hint=auto \\
  ${URL}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=graphical-session.target
EOF

systemctl --user daemon-reload
systemctl --user enable pingway-kiosk.service
sudo loginctl enable-linger "$USER"

echo
echo "Done. Start now with:  systemctl --user start pingway-kiosk"
echo "It will start automatically on boot (graphical session)."
echo "URL: ${URL}   (append ?theme=light or ?scale=1.2 to taste)"
