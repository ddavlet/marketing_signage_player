package system

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

type OSInfo struct {
	ID         string `json:"id"`
	PrettyName string `json:"pretty_name"`
	Version    string `json:"version"`
	Kernel     string `json:"kernel"`
	Arch       string `json:"arch"`
	GOOS       string `json:"goos"`
}

func ReadOSInfo() OSInfo {
	info := OSInfo{
		Arch: runtime.GOARCH,
		GOOS: runtime.GOOS,
	}
	parseOSRelease("/etc/os-release", &info)
	info.Kernel = readKernel()
	return info
}

func parseOSRelease(path string, info *OSInfo) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			info.ID = v
		case "PRETTY_NAME":
			info.PrettyName = v
		case "VERSION":
			info.Version = v
		}
	}
}

func readKernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
