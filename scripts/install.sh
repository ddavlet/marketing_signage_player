#!/bin/sh
# install.sh — idempotent installer for the Marketing Signage Player agent.
#
# Usage (run as root):
#   curl -sSf https://<server>/static/player/install.sh | sudo bash -s -- --server=https://<server>
#
# Requirements: Debian/Ubuntu with apt, systemd, an X server, python3.
set -eu

# ── argument parsing ────────────────────────────────────────────────────────

SERVER_URL=""
CHANNEL="stable"
DATA_DIR="/var/lib/marketing-signage"
CONFIG_DIR="/etc/marketing-signage"
BINARY="/usr/local/bin/marketing-signage-player"
SERVICE="marketing-signage-player"
SIGNAGE_USER="signage"

for arg in "$@"; do
  case "$arg" in
    --server=*)  SERVER_URL="${arg#*=}" ;;
    --channel=*) CHANNEL="${arg#*=}" ;;
    *)           echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

if [ -z "$SERVER_URL" ]; then
  echo "Error: --server=<url> is required" >&2
  exit 1
fi
SERVER_URL="${SERVER_URL%/}"  # strip trailing slash

# ── root check ──────────────────────────────────────────────────────────────

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: run this script as root (sudo)" >&2
  exit 1
fi

# ── helpers ─────────────────────────────────────────────────────────────────

log() { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }

detect_arch() {
  case "$(uname -m)" in
    x86_64)  echo amd64 ;;
    aarch64) echo arm64 ;;
    armv7l)  echo arm ;;
    *)       echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

# ── 1. dependencies ─────────────────────────────────────────────────────────

log "Installing system dependencies…"
apt-get update -qq
apt-get install -y -qq \
  chromium \
  curl \
  ca-certificates \
  x11-xserver-utils \
  xserver-xorg \
  xinit \
  unclutter \
  openbox

# ── 2. display manager (if not already present) ─────────────────────────────

# Check if a display manager is already active (full desktop install).
# systemctl list-units covers gdm, lightdm, sddm, xdm, etc.
if systemctl list-units --type=service --state=active 2>/dev/null | grep -qE 'gdm|lightdm|sddm|xdm|wdm'; then
  log "Display manager already running — skipping lightdm setup."
else
  log "No display manager found — installing lightdm for auto-login…"
  apt-get install -y -qq lightdm

  # Auto-login as signage user into an openbox session.
  mkdir -p /etc/lightdm/lightdm.conf.d
  cat > /etc/lightdm/lightdm.conf.d/50-signage.conf <<EOF
[Seat:*]
autologin-user=${SIGNAGE_USER}
autologin-user-timeout=0
user-session=openbox
EOF

  # Openbox session file so lightdm knows what to launch.
  mkdir -p /usr/share/xsessions
  if [ ! -f /usr/share/xsessions/openbox.desktop ]; then
    cat > /usr/share/xsessions/openbox.desktop <<EOF
[Desktop Entry]
Name=Openbox
Comment=Openbox window manager
Exec=openbox-session
Type=Application
EOF
  fi

  systemctl enable lightdm
  log "lightdm installed and enabled."
fi

# ── 4. system user ──────────────────────────────────────────────────────────

if ! id "$SIGNAGE_USER" >/dev/null 2>&1; then
  log "Creating system user '$SIGNAGE_USER'…"
  useradd -r -m -d "$DATA_DIR" -s /usr/sbin/nologin "$SIGNAGE_USER"
else
  log "User '$SIGNAGE_USER' already exists — skipping."
fi

# ── 5. download binary ──────────────────────────────────────────────────────

ARCH="$(detect_arch)"

log "Fetching release info for linux/$ARCH (channel: $CHANNEL)…"
RELEASE_JSON="$(curl -fsSL "${SERVER_URL}/api/player/releases/latest/?channel=${CHANNEL}&os=linux&arch=${ARCH}")"

BINARY_URL="$(echo "$RELEASE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['download_url'])")"
EXPECTED_SHA256="$(echo "$RELEASE_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['sha256'])")"

if [ -z "$BINARY_URL" ]; then
  echo "Error: no release found for linux/$ARCH on channel '$CHANNEL'" >&2
  exit 1
fi

log "Downloading player agent from $BINARY_URL…"
curl -fsSL "$BINARY_URL" -o "${BINARY}.new"
chmod 0755 "${BINARY}.new"

ACTUAL_SHA256="$(sha256sum "${BINARY}.new" | awk '{print $1}')"
if [ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]; then
  echo "Error: SHA256 mismatch (got $ACTUAL_SHA256, expected $EXPECTED_SHA256)" >&2
  rm -f "${BINARY}.new"
  exit 1
fi

mv "${BINARY}.new" "$BINARY"
log "Installed: $BINARY"

# ── 6. config ───────────────────────────────────────────────────────────────

mkdir -p "$CONFIG_DIR"

# Download fallback image (shown when the server is unreachable).
FALLBACK_IMAGE="${CONFIG_DIR}/fallback.png"
FALLBACK_IMAGE_URL="${SERVER_URL}/static/player/fallback.png"
log "Downloading fallback image…"
if curl -fsSL "$FALLBACK_IMAGE_URL" -o "$FALLBACK_IMAGE" 2>/dev/null; then
  log "Fallback image saved to $FALLBACK_IMAGE"
else
  log "No fallback image found on server — plain black screen will be used."
  FALLBACK_IMAGE=""
fi

if [ ! -f "${CONFIG_DIR}/config.toml" ]; then
  log "Writing initial config…"
  cat > "${CONFIG_DIR}/config.toml" <<EOF
server_url     = "${SERVER_URL}"
device_key     = ""
update_channel = "${CHANNEL}"
log_level      = "info"
chromium_path  = ""
data_dir       = "${DATA_DIR}"
fallback_image = "${FALLBACK_IMAGE}"
EOF
  chmod 0640 "${CONFIG_DIR}/config.toml"
else
  log "Config already exists — skipping (update server_url manually if needed)."
fi

# ── 7. systemd unit ─────────────────────────────────────────────────────────

UNIT_FILE="/etc/systemd/system/${SERVICE}.service"
cat > "$UNIT_FILE" <<'EOF'
[Unit]
Description=Marketing Signage Player Agent
Documentation=https://signage.example.com/docs/player
After=network-online.target graphical.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/marketing-signage-player --config /etc/marketing-signage/config.toml
Restart=always
RestartSec=5s
Environment=DISPLAY=:0
Environment=XAUTHORITY=/home/signage/.Xauthority
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=graphical.target
EOF

systemctl daemon-reload
systemctl enable --now "$SERVICE"
log "Service enabled and started."

# ── 8. print hardware-id for operator ───────────────────────────────────────

HWID="$("$BINARY" --print-hwid 2>/dev/null || echo "(run '$BINARY --print-hwid' as root)")"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Installation complete!"
echo ""
echo " Hardware ID: ${HWID}"
echo ""
echo " The device will appear as PENDING in the control panel."
echo " Open ${SERVER_URL}/devices/pending to approve it."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
