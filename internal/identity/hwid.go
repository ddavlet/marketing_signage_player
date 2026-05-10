package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"sort"
	"strings"
)

// HardwareID returns a stable identifier for this device.
// /etc/machine-id alone can be regenerated on disk-image cloning,
// so we mix in the lowest stable MAC address.
func HardwareID() string {
	h := sha256.New()
	if mid, err := os.ReadFile("/etc/machine-id"); err == nil {
		h.Write([]byte(strings.TrimSpace(string(mid))))
	} else if mid, err := os.ReadFile("/var/lib/dbus/machine-id"); err == nil {
		h.Write([]byte(strings.TrimSpace(string(mid))))
	}
	if mac := stableMAC(); mac != "" {
		h.Write([]byte("|"))
		h.Write([]byte(mac))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func stableMAC() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var addrs []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifc.HardwareAddr) == 0 {
			continue
		}
		name := ifc.Name
		if strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "virbr") {
			continue
		}
		addrs = append(addrs, ifc.HardwareAddr.String())
	}
	sort.Strings(addrs)
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
