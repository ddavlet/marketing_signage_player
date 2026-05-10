// Package updater polls the server for a newer player binary, verifies its
// SHA256, atomically replaces the running executable, and restarts via systemd.
package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	gomod "golang.org/x/mod/semver"

	"github.com/marketing-signage/player/internal/api"
	"github.com/marketing-signage/player/internal/system"
)

// ErrUpToDate is returned when the current version is already the latest.
var ErrUpToDate = errors.New("already up to date")

// Releases is the subset of api.Client used by the updater.
// Declared here so the updater package does not depend on the full client.
type Releases interface {
	LatestRelease(ctx context.Context, channel, os, arch string) (*api.Release, error)
}

// Restarter restarts the current systemd service unit.
// The default implementation calls `systemctl restart marketing-signage-player`.
type Restarter interface {
	Restart() error
}

// SystemdRestarter is the production Restarter.
type SystemdRestarter struct{ Unit string }

func (s SystemdRestarter) Restart() error {
	return exec.Command("systemctl", "restart", s.unit()).Run()
}

func (s SystemdRestarter) unit() string {
	if s.Unit != "" {
		return s.Unit
	}
	return "marketing-signage-player"
}

// Updater checks for new releases on a ticker and applies them atomically.
type Updater struct {
	releases      Releases
	httpClient    *http.Client
	restarter     Restarter
	channel       func() string // called each check to get the active channel
	checkInterval time.Duration
	log           *slog.Logger
}

// Options configures an Updater.
type Options struct {
	Releases      Releases
	HTTPClient    *http.Client
	Restarter     Restarter    // nil → SystemdRestarter with default unit name
	Channel       func() string
	CheckInterval time.Duration // nil → 15 min
	Log           *slog.Logger
}

// New returns an Updater. Channel must not be nil.
func New(opts Options) *Updater {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.CheckInterval == 0 {
		opts.CheckInterval = 15 * time.Minute
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if opts.Restarter == nil {
		opts.Restarter = SystemdRestarter{}
	}
	return &Updater{
		releases:      opts.Releases,
		httpClient:    opts.HTTPClient,
		restarter:     opts.Restarter,
		channel:       opts.Channel,
		checkInterval: opts.CheckInterval,
		log:           opts.Log.With("subsystem", "updater"),
	}
}

// Run checks immediately then on the configured interval. It satisfies
// runtime.Subsystem. When an update is applied the process is replaced by
// systemd so Run does not return in the normal case.
func (u *Updater) Run(ctx context.Context) error {
	u.check(ctx)

	t := time.NewTicker(u.checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			u.check(ctx)
		}
	}
}

func (u *Updater) check(ctx context.Context) {
	err := u.applyIfNewer(ctx)
	switch {
	case err == nil:
		// systemctl restart called; we won't normally reach here
	case errors.Is(err, ErrUpToDate):
		u.log.Debug("up to date", slog.String("version", system.Version))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	default:
		u.log.Warn("update check failed", slog.String("error", err.Error()))
	}
}

func (u *Updater) applyIfNewer(ctx context.Context) error {
	release, err := u.releases.LatestRelease(ctx, u.channel(), "linux", runtime.GOARCH)
	if errors.Is(err, api.ErrNotFound) {
		return ErrUpToDate // no releases published yet
	}
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}

	cur := normVer(system.Version)
	latest := normVer(release.Version)
	if cur == "" || latest == "" {
		// Non-semver version (e.g. "dev") — skip update.
		u.log.Debug("skipping update: non-semver version",
			slog.String("current", system.Version),
			slog.String("latest", release.Version))
		return ErrUpToDate
	}
	if gomod.Compare(cur, latest) >= 0 {
		return ErrUpToDate
	}

	u.log.Info("update available",
		slog.String("current", system.Version),
		slog.String("latest", release.Version))

	if err := u.download(ctx, release); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	u.log.Info("binary replaced — restarting service", slog.String("version", release.Version))
	return u.restarter.Restart()
}

// download fetches the release binary, verifies its SHA256, and atomically
// replaces the running executable. The temp file is placed in the same
// directory as the executable to guarantee an atomic rename (same filesystem).
func (u *Updater) download(ctx context.Context, release *api.Release) error {
	exe, err := resolveExe()
	if err != nil {
		return err
	}

	tmp := exe + ".new"
	defer os.Remove(tmp) // clean up on any error

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.DownloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", release.DownloadURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", release.DownloadURL, resp.StatusCode)
	}

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	f.Close()

	if got, want := fmt.Sprintf("%x", h.Sum(nil)), release.SHA256; got != want {
		return fmt.Errorf("SHA256 mismatch: got %s want %s", got, want)
	}
	u.log.Debug("SHA256 verified", slog.String("sha256", release.SHA256))

	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

func resolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlink: %w", err)
	}
	return exe, nil
}

// normVer returns the version prefixed with "v" for golang.org/x/mod/semver,
// or "" if the version is not semver (e.g. "dev").
func normVer(v string) string {
	if v == "" || v == "dev" {
		return ""
	}
	if v[0] != 'v' {
		v = "v" + v
	}
	if !gomod.IsValid(v) {
		return ""
	}
	return v
}
