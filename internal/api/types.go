package api

import (
	"encoding/json"
	"time"
)

// RegisterRequest is sent on first boot when the device has no device_key.
type RegisterRequest struct {
	HardwareID    string          `json:"hardware_id"`
	Hostname      string          `json:"hostname"`
	OSInfo        json.RawMessage `json:"os_info"`
	PlayerVersion string          `json:"player_version"`
	SSHPort       int             `json:"ssh_port,omitempty"`
}

// RegisterResponse is "pending" until an admin approves the device,
// at which point it returns "approved" with the device_key.
type RegisterResponse struct {
	Status    string `json:"status"`
	DeviceKey string `json:"device_key,omitempty"`
}

const (
	RegisterStatusPending  = "pending"
	RegisterStatusApproved = "approved"
)

// ScreenSchedule mirrors the panel's per-device screen on/off times.
type ScreenSchedule struct {
	On       string `json:"on"`
	Off      string `json:"off"`
	Timezone string `json:"tz"`
}

// Command is a one-shot instruction sent from the panel to the agent.
type Command struct {
	ID      int             `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// HeartbeatResponse extends the existing heartbeat with player-controlled fields.
type HeartbeatResponse struct {
	PlaylistVersion     int            `json:"playlist_version"`
	ServerTime          time.Time      `json:"server_time"`
	SyncIntervalSeconds int            `json:"sync_interval_seconds"`
	UpdateChannel       string         `json:"update_channel"`
	ScreenSchedule      ScreenSchedule `json:"screen_schedule"`
	Commands            []Command      `json:"commands"`
}

// Release describes a player binary available for download.
type Release struct {
	Version     string `json:"version"`
	Channel     string `json:"channel"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Signature   string `json:"signature,omitempty"`
	Notes       string `json:"notes,omitempty"`
}
