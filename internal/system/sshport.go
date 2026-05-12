package system

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var reverseTunnelRe = regexp.MustCompile(`-R\s+(\d+):`)

// DetectSSHTunnelPort scans /etc/systemd/system/*.service for a reverse SSH
// tunnel flag (-R PORT:...) and returns the first port found, or 0 if none.
func DetectSSHTunnelPort() int {
	matches, err := filepath.Glob("/etc/systemd/system/*.service")
	if err != nil {
		return 0
	}
	for _, path := range matches {
		if port := sshPortFromUnit(path); port != 0 {
			return port
		}
	}
	return 0
}

func sshPortFromUnit(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(strings.TrimSpace(line), "ExecStart") {
			continue
		}
		m := reverseTunnelRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p, err := strconv.Atoi(m[1])
		if err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	return 0
}
