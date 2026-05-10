//go:build linux

package scheduler

import "os/exec"

// setDisplay sends a DPMS force on/off command via xset.
// DISPLAY and XAUTHORITY must be set in the environment (done by the systemd unit).
func setDisplay(on bool) error {
	arg := "on"
	if !on {
		arg = "off"
	}
	return exec.Command("xset", "dpms", "force", arg).Run()
}
