# Marketing Signage Player — Installation Guide

## Overview

The player agent is a background service that runs on Debian 13 devices. It:

- Opens Chromium in kiosk mode pointed at the control panel's player page
- Registers itself on first boot and waits for admin approval
- Receives playlist, sync interval, and screen schedule settings from the panel
- Restarts Chromium automatically if it crashes
- Self-updates when a new release is published

All content management (playlists, media, schedules) is done from the control panel. The device has no UI of its own.

---

## Requirements

**Device:**

- Debian 13 (Bookworm) or later
- x86-64 or ARM64 CPU
- 1 GB RAM minimum
- Display connected
- Internet access to the control panel

**Server:**

- Control panel running and reachable from the device
- Admin account on the panel

---

## Part 1 — Prepare the server

### 1.1 Build the release binaries

On your development machine:

```bash
cd marketing_signage_player
make release-all VER=v1.0.0
```

This produces:

```
dist/
├── marketing-signage-player-linux-amd64
├── marketing-signage-player-linux-arm64
└── SHA256SUMS
```

### 1.2 Serve the binaries

The install script downloads the binary from `{server}/static/player/`. Copy the binaries to your web server's static directory, or serve them temporarily:

```bash
mkdir -p /tmp/player-serve/static/player
cp dist/marketing-signage-player-linux-* /tmp/player-serve/static/player/
cp scripts/install.sh /tmp/player-serve/static/
cd /tmp/player-serve && python3 -m http.server 8888
```

> For production, serve the binaries from your actual web server or S3. The URL must be reachable from the device.

---

## Part 2 — Install on the device

Run this on the device as root. Replace `https://signage.example.com` with your actual server URL.

```bash
curl -sSf https://signage.example.com/static/install.sh | sudo bash -s -- \
  --server=https://signage.example.com
```

### What the installer does

1. Installs system packages: `chromium`, `xserver-xorg`, `xinit`, `openbox`, `unclutter`
2. Creates a `signage` system user at `/var/lib/marketing-signage`
3. Downloads the correct binary for the device architecture
4. Writes `/etc/marketing-signage/config.toml`
5. Installs and starts the `marketing-signage-player` systemd service
6. Prints the device's hardware ID

### Manual installation (without the script)

If you prefer to install step by step:

```bash
# 1. Install dependencies
sudo apt-get update
sudo apt-get install -y chromium curl ca-certificates \
  x11-xserver-utils xserver-xorg xinit unclutter openbox

# 2. Create system user
sudo useradd -r -m -d /var/lib/marketing-signage -s /usr/sbin/nologin signage

# 3. Download binary (replace amd64 with arm64 if needed)
sudo curl -fsSL https://signage.example.com/static/player/marketing-signage-player-linux-amd64 \
  -o /usr/local/bin/marketing-signage-player
sudo chmod 0755 /usr/local/bin/marketing-signage-player

# 4. Write config
sudo mkdir -p /etc/marketing-signage
sudo tee /etc/marketing-signage/config.toml << 'EOF'
server_url     = "https://signage.example.com"
device_key     = ""
update_channel = "stable"
log_level      = "info"
data_dir       = "/var/lib/marketing-signage"
EOF

# 5. Install systemd service
sudo tee /etc/systemd/system/marketing-signage-player.service << 'EOF'
[Unit]
Description=Marketing Signage Player Agent
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

sudo systemctl daemon-reload
sudo systemctl enable --now marketing-signage-player
```

---

## Part 3 — Approve the device in the panel

After the agent starts it registers itself and waits for approval.

1. Open the control panel and go to **Devices → Pending**
2. Find the device by hostname or hardware ID
3. Click **Approve**
4. Select a **Location** and **Playlist**
5. Click **Submit**

The agent polls every 30 seconds. Within 30 seconds of approval it will:

- Persist the device key to config
- Launch Chromium in kiosk mode
- Start sending heartbeats

You should see the device status change to **online** in the panel.

---

## Part 4 — Verify

### Check the agent is running

```bash
sudo journalctl -u marketing-signage-player -f
```

Expected output after approval:

```
{"msg":"device approved and paired"}
{"msg":"chromium supervisor ready","kiosk_url":"https://signage.example.com/player/.../"}
{"msg":"starting chromium"}
{"msg":"display toggled","on":true}
{"msg":"heartbeat ok"}
```

### Check the hardware ID

```bash
/usr/local/bin/marketing-signage-player --print-hwid
```

---

## Part 5 — Managing devices from the panel

### Device settings (Devices → select device)


| Setting                | Description                                                 |
| ---------------------- | ----------------------------------------------------------- |
| **Sync interval**      | How often the agent heartbeats, in seconds (default 60)     |
| **Update channel**     | `stable` or `beta`                                          |
| **Screen on/off time** | Daily on/off window, e.g. 07:00–23:00 (switches kiosk vs black page in Chromium). Leave blank for always on. |
| **Timezone**           | IANA timezone for the screen schedule, e.g. `Asia/Tashkent` |


### Remote commands

From the device detail page you can send:

- **Restart Chromium** — kills and relaunches the browser without rebooting
- **Reboot** — reboots the device via systemd

Commands are delivered on the next heartbeat (within `sync_interval_seconds`).

### Player releases (Releases page)

To push an update to all devices:

1. Build new binaries: `make release-all VER=v1.2.0`
2. Upload to your static file server
3. Open **Releases** in the panel and create a new release entry (version, channel, OS, arch, download URL, SHA256)
4. Set it to **active**

Devices on the matching channel pick it up within 15 minutes, verify the SHA256, replace the binary, and restart the service automatically.

Or use `make upload` to register the release directly from the terminal:

```bash
SIGNAGE_SERVER=https://signage.example.com \
SIGNAGE_TOKEN=your-api-token \
make upload VER=v1.2.0
```

---

## Troubleshooting

### Device does not appear in Pending

- Check the agent is running: `sudo systemctl status marketing-signage-player`
- Check the server URL in config: `sudo cat /etc/marketing-signage/config.toml`
- Check network: `curl -I https://signage.example.com`

### Chromium does not open

- Confirm an X server is running: `echo $DISPLAY`
- Check `DISPLAY=:0` is set in the service environment
- Try launching manually: `DISPLAY=:0 chromium --kiosk https://signage.example.com/player/.../`

### Device shows offline after approval

- Check heartbeat errors in the journal: `sudo journalctl -u marketing-signage-player | grep -i error`
- Confirm the device can reach the server: `curl https://signage.example.com/api/device/heartbeat/`

### Reset and re-register

Wipe the device key to start the registration flow again:

```bash
sudo sed -i 's/^device_key.*/device_key = ""/' /etc/marketing-signage/config.toml
sudo systemctl restart marketing-signage-player
```

The old device entry in the panel can be deleted or reused — the agent will create a new pending entry.

### Uninstall

```bash
sudo systemctl disable --now marketing-signage-player
sudo rm /usr/local/bin/marketing-signage-player
sudo rm /etc/systemd/system/marketing-signage-player.service
sudo rm -rf /etc/marketing-signage /var/lib/marketing-signage
sudo userdel signage
sudo systemctl daemon-reload
```

---

## Config file reference

`/etc/marketing-signage/config.toml`


| Key              | Default                        | Description                                           |
| ---------------- | ------------------------------ | ----------------------------------------------------- |
| `server_url`     | —                              | Control panel base URL (required)                     |
| `device_key`     | `""`                           | Filled automatically after approval — do not edit     |
| `update_channel` | `"stable"`                     | Release channel: `stable` or `beta`                   |
| `log_level`      | `"info"`                       | Log verbosity: `debug`, `info`, `warn`, `error`       |
| `chromium_path`  | `""`                           | Override Chromium binary path; auto-detected if empty |
| `data_dir`       | `"/var/lib/marketing-signage"` | Directory for Chromium profile and cache              |


