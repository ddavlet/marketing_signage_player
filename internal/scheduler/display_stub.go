//go:build !linux

package scheduler

import "log/slog"

// setDisplay is a no-op on non-Linux platforms (dev / CI).
func setDisplay(on bool) error {
	slog.Debug("display stub: would set display", slog.Bool("on", on))
	return nil
}
