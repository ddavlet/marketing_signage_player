// Package supervisor manages the Chromium kiosk subprocess on the device.
// It spawns the browser, watches it, and restarts on crash with bounded
// exponential backoff. The browser is the thing the operator actually sees;
// the agent has no UI of its own.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options configures a Supervisor.
type Options struct {
	// BinaryPath is the chromium/chrome executable. If empty, it is
	// auto-detected from PATH and well-known macOS/Linux locations.
	BinaryPath string

	// KioskURL is the URL Chromium loads, e.g.
	// https://signage.example.com/player/<device_key>/
	KioskURL string

	// UserDataDir is Chromium's --user-data-dir. Persisting it across
	// restarts is essential — that's where the Service Worker, its cache,
	// and localStorage live, which is what makes offline playback work.
	UserDataDir string

	// ExtraArgs are appended after the default kiosk flags. Useful for
	// dev (e.g. --remote-debugging-port=9222) or hardware quirks.
	ExtraArgs []string

	// MinRunDurationToReset is how long Chromium must run before a clean
	// exit resets the restart backoff. Defaults to 30s.
	MinRunDurationToReset time.Duration

	// InitialBackoff and MaxBackoff bound the exponential restart timer.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration

	Log *slog.Logger
}

// Supervisor owns the Chromium subprocess lifecycle.
type Supervisor struct {
	opts      Options
	mu        sync.Mutex
	current   *exec.Cmd
	activeURL string // guarded by mu; may differ from opts.KioskURL after SwitchURL
}

// SwitchURL changes the URL Chromium will load on its next (re)start and
// kills the current process so the supervisor loop relaunches immediately.
func (s *Supervisor) SwitchURL(u string) error {
	s.mu.Lock()
	s.activeURL = u
	s.mu.Unlock()
	return s.KillCurrent()
}

// KillCurrent sends SIGINT to the current Chromium process tree and
// escalates to SIGKILL after 10 s if the process has not exited.
//
// Two-pronged approach: try the process group first (works when Setpgid
// is effective), then always walk /proc to kill every process in the tree
// directly. This handles the case where the child wrapper ends up in the
// parent's PGID instead of its own, which makes the group signal a no-op.
func (s *Supervisor) KillCurrent() error {
	s.mu.Lock()
	cmd := s.current
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGINT)
	killTree(pid, syscall.SIGINT)
	go func() {
		time.Sleep(10 * time.Second)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		killTree(pid, syscall.SIGKILL)
	}()
	return nil
}

// killTree sends sig to every process in the subtree rooted at pid.
// Children are signalled before the parent so they cannot re-spawn.
// Reads /proc/<pid>/task/<pid>/children (Linux) to discover children.
func killTree(pid int, sig syscall.Signal) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err == nil {
		for _, s := range strings.Fields(string(data)) {
			if child, err := strconv.Atoi(s); err == nil && child > 0 {
				killTree(child, sig)
			}
		}
	}
	_ = syscall.Kill(pid, sig)
}

// New validates options and detects the Chromium binary if needed.
func New(opts Options) (*Supervisor, error) {
	if opts.KioskURL == "" {
		return nil, errors.New("kiosk url required")
	}
	if opts.BinaryPath == "" {
		path, err := DetectBinary()
		if err != nil {
			return nil, err
		}
		opts.BinaryPath = path
	}
	if opts.UserDataDir == "" {
		return nil, errors.New("user data dir required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.MinRunDurationToReset == 0 {
		opts.MinRunDurationToReset = 30 * time.Second
	}
	if opts.InitialBackoff == 0 {
		opts.InitialBackoff = 1 * time.Second
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 60 * time.Second
	}
	return &Supervisor{opts: opts, activeURL: opts.KioskURL}, nil
}

// Run blocks until ctx is cancelled. It spawns Chromium, waits for it to
// exit, and restarts with exponential backoff on crash. Clean exits that
// happened after MinRunDurationToReset reset the backoff to InitialBackoff.
func (s *Supervisor) Run(ctx context.Context) error {
	if err := os.MkdirAll(s.opts.UserDataDir, 0o755); err != nil {
		return fmt.Errorf("ensure user-data-dir: %w", err)
	}

	wait := s.opts.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		args := s.buildArgs()
		s.opts.Log.Info("starting chromium",
			slog.String("binary", s.opts.BinaryPath),
			slog.String("url", args[len(args)-1]))

		cmd := exec.CommandContext(ctx, s.opts.BinaryPath, args...)
		cmd.Env = os.Environ()
		// Put the child in its own process group so KillCurrent can send a
		// signal to the whole group (wrapper script + browser children).
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
			killTree(cmd.Process.Pid, syscall.SIGINT)
			return nil
		}
		cmd.WaitDelay = 5 * time.Second

		s.mu.Lock()
		s.current = cmd
		s.mu.Unlock()

		start := time.Now()
		err := cmd.Run()

		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
		elapsed := time.Since(start)

		// Context cancellation is shutdown, not crash — return cleanly.
		if cerr := ctx.Err(); cerr != nil {
			s.opts.Log.Info("supervisor shutting down",
				slog.Duration("uptime", elapsed))
			return cerr
		}

		switch {
		case err == nil:
			s.opts.Log.Info("chromium exited cleanly",
				slog.Duration("uptime", elapsed))
		default:
			s.opts.Log.Warn("chromium crashed",
				slog.String("error", err.Error()),
				slog.Duration("uptime", elapsed))
		}

		if elapsed >= s.opts.MinRunDurationToReset {
			wait = s.opts.InitialBackoff
		}

		nextWait := jitter(wait, 0.25)
		s.opts.Log.Info("restarting chromium", slog.Duration("after", nextWait))

		t := time.NewTimer(nextWait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}

		wait *= 2
		if wait > s.opts.MaxBackoff {
			wait = s.opts.MaxBackoff
		}
	}
}

// buildArgs constructs the Chromium command line. The flag set is the
// proven-stable kiosk recipe: no first-run prompts, no infobars, no
// translate, no extensions. The user-data-dir is the one knob the
// operator changes most often.
func (s *Supervisor) buildArgs() []string {
	s.mu.Lock()
	u := s.activeURL
	s.mu.Unlock()

	args := []string{
		// Kiosk basics
		"--kiosk",
		"--noerrdialogs",
		"--disable-infobars",
		"--no-first-run",
		"--no-default-browser-check",
		"--lang=en",

		// Signage-critical: allow video/audio to autoplay without user gesture
		"--autoplay-policy=no-user-gesture-required",

		// Suppress dialogs that would block the screen
		"--disable-session-crashed-bubble", // no "Restore pages?" after watchdog kill
		"--disable-hang-monitor",           // no "Page unresponsive" popup

		// Keep JS timers and renderer at full rate even when "backgrounded"
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",

		// Stability on ARM / devices with small /dev/shm
		"--disable-dev-shm-usage",

		// Avoid OS keychain prompts on Linux
		"--password-store=basic",

		// Stop Chrome from self-updating (we manage updates via the agent)
		"--check-for-update-interval=31536000",

		// Account / network noise
		"--disable-sync",
		"--disable-extensions",
		"--disable-translate",
		"--disable-features=TranslateUI",

		"--user-data-dir=" + s.opts.UserDataDir,
	}
	args = append(args, s.opts.ExtraArgs...)
	args = append(args, u)
	return args
}

// DetectBinary finds a Chromium-flavored browser on the host. It searches
// PATH first (so a deliberately-installed binary wins) and falls back to
// well-known absolute paths on macOS.
func DetectBinary() (string, error) {
	candidates := []string{
		"chromium",
		"chromium-browser",
		"google-chrome",
		"google-chrome-stable",
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		)
	}

	for _, c := range candidates {
		if filepath.IsAbs(c) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c, nil
			}
			continue
		}
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no chromium/chrome binary found in PATH or known locations (tried: %s)",
		strings.Join(candidates, ", "))
}

func jitter(d time.Duration, frac float64) time.Duration {
	if d <= 0 {
		return d
	}
	span := float64(d) * frac
	delta := time.Duration((rand.Float64()*2 - 1) * span)
	return d + delta
}
