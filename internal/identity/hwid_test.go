package identity

import "testing"

func TestHardwareIDStable(t *testing.T) {
	a := HardwareID()
	b := HardwareID()
	if a != b {
		t.Errorf("hardware id not stable: %q vs %q", a, b)
	}
	if len(a) != 32 {
		t.Errorf("expected 32-hex-char id, got %d chars (%q)", len(a), a)
	}
}
