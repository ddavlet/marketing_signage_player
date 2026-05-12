package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// helperScript writes a tiny POSIX shell script that appends a line to
// counterPath each time it's invoked, then exits. We use it as a stand-in
// for Chromium so we can observe restart behavior without spawning a real
// browser.
func helperScript(t *testing.T, counterPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "helper.sh")
	body := "#!/bin/sh\necho hit >> " + counterPath + "\nexit 0\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRestartsAfterExit(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "counter")
	bin := helperScript(t, counter)

	sv, err := New(Options{
		BinaryPath:            bin,
		KioskURL:              "https://example.test/player/x/",
		UserDataDir:           t.TempDir(),
		Log:                   quietLogger(),
		InitialBackoff:        10 * time.Millisecond,
		MaxBackoff:            20 * time.Millisecond,
		MinRunDurationToReset: 1, // reset backoff every clean exit so restarts stay fast
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sv.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}

	data, _ := os.ReadFile(counter)
	hits := strings.Count(string(data), "hit")
	if hits < 3 {
		t.Errorf("expected supervisor to restart helper at least 3 times before deadline, got %d", hits)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not in PATH: %v", err)
	}

	sv, err := New(Options{
		BinaryPath:     bin,
		ExtraArgs:      []string{"30"},
		KioskURL:       "https://example.test/player/x/",
		UserDataDir:    t.TempDir(),
		Log:            quietLogger(),
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sv.Run(ctx) }()

	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled, got: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("supervisor did not shut down within 7s of cancel")
	}
}

func TestNewRequiresKioskURL(t *testing.T) {
	if _, err := New(Options{
		BinaryPath:  "/usr/bin/true",
		UserDataDir: t.TempDir(),
	}); err == nil {
		t.Fatal("expected error when KioskURL is empty")
	}
}

func TestNewRequiresUserDataDir(t *testing.T) {
	if _, err := New(Options{
		BinaryPath: "/usr/bin/true",
		KioskURL:   "https://example.test/",
	}); err == nil {
		t.Fatal("expected error when UserDataDir is empty")
	}
}

func TestBuildArgsContainsKioskURL(t *testing.T) {
	sv, err := New(Options{
		BinaryPath:  "/usr/bin/true",
		KioskURL:    "https://example.test/player/abc/",
		UserDataDir: t.TempDir(),
		Log:         quietLogger(),
		ExtraArgs:   []string{"--remote-debugging-port=9222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := sv.buildArgs()
	if args[len(args)-1] != "https://example.test/player/abc/" {
		t.Errorf("expected kiosk URL last, got: %v", args)
	}
	found := false
	for _, a := range args {
		if a == "--remote-debugging-port=9222" {
			found = true
		}
	}
	if !found {
		t.Errorf("ExtraArgs not present in args: %v", args)
	}
}
