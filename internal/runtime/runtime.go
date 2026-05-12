// Package runtime is the orchestrator. It runs the heartbeat loop and the
// Chromium supervisor concurrently, with cancel-on-first-error semantics
// so a 401 (key wiped server-side) tears the supervisor down too and we
// re-enter pairing.
//
// Heartbeat-driven fallback (local file:// page) runs at most once: only
// when the very first heartbeat fails with connectivity-class errors
// (transport, 404, 5xx). Later failures only log — Chromium keeps showing
// the kiosk page and cached content when the panel drops out. 401 always
// surfaces for re-pair.
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/marketing-signage/player/internal/api"
)

// Subsystem is the minimal interface the runtime needs from its children.
// internal/supervisor.Supervisor satisfies this; tests can pass fakes.
type Subsystem interface {
	Run(ctx context.Context) error
}

// ScheduleUpdater is satisfied by *scheduler.Scheduler. The runtime calls
// Update on every successful heartbeat so the scheduler always has current
// on/off times without polling the server itself.
type ScheduleUpdater interface {
	Update(sched api.ScreenSchedule)
	Run(ctx context.Context) error
}

// Commander is satisfied by *supervisor.Supervisor. It lets the runtime
// restart Chromium in response to a panel command without importing supervisor.
type Commander interface {
	KillCurrent() error
	SwitchURL(string) error
}

type Options struct {
	Client          *api.Client
	Supervisor      Subsystem
	Commander       Commander // optional; nil if no supervisor
	Scheduler       ScheduleUpdater
	Updater         Subsystem
	Log             *slog.Logger
	DefaultInterval time.Duration
	KioskURL        string // original kiosk URL to restore after fallback
	FallbackURL     string // local file:// page; only first failed heartbeat may use it; empty disables
}

// Run blocks until ctx is cancelled or a subsystem returns a fatal error
// (e.g. ErrUnauthorized from heartbeat). Returns the first non-cancellation
// error if any subsystem hit one.
func Run(ctx context.Context, opts Options) error {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		fatal   error
	)
	finish := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			errOnce.Do(func() { fatal = err })
		}
		cancel() // tear other subsystems down
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		finish(heartbeatLoop(ctx, opts))
	}()

	if opts.Supervisor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finish(opts.Supervisor.Run(ctx))
		}()
	}

	if opts.Scheduler != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finish(opts.Scheduler.Run(ctx))
		}()
	}

	if opts.Updater != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finish(opts.Updater.Run(ctx))
		}()
	}

	wg.Wait()
	if fatal != nil {
		return fatal
	}
	return ctx.Err()
}

// heartbeatConnectivityLoss reports errors that mean the panel did not give
// a usable heartbeat response (excluding 401, which is handled separately).
func heartbeatConnectivityLoss(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, api.ErrNotFound) ||
		errors.Is(err, api.ErrHTTPError) ||
		errors.Is(err, api.ErrTransport)
}

func dispatchCommands(ctx context.Context, opts Options, cmds []api.Command) {
	for _, cmd := range cmds {
		opts.Log.Info("executing command", slog.String("kind", cmd.Kind), slog.Int("id", cmd.ID))
		var execErr error
		switch cmd.Kind {
		case "restart_chromium":
			if opts.Commander != nil {
				execErr = opts.Commander.KillCurrent()
			}
		case "reboot":
			execErr = exec.CommandContext(ctx, "systemctl", "reboot").Run()
		default:
			opts.Log.Warn("unknown command kind", slog.String("kind", cmd.Kind))
		}
		if execErr != nil {
			opts.Log.Warn("command execution failed",
				slog.String("kind", cmd.Kind),
				slog.String("error", execErr.Error()))
		} else {
			opts.Log.Info("command executed", slog.String("kind", cmd.Kind), slog.Int("id", cmd.ID))
		}
		if err := opts.Client.AckCommand(ctx, cmd.ID); err != nil {
			opts.Log.Warn("command ack failed",
				slog.Int("id", cmd.ID),
				slog.String("error", err.Error()))
		}
	}
}

func heartbeatLoop(ctx context.Context, opts Options) error {
	interval := opts.DefaultInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}

	canFallback := opts.Commander != nil && opts.FallbackURL != ""
	inFallback := false
	firstHeartbeat := true

	for {
		isInitialHeartbeat := firstHeartbeat
		resp, err := opts.Client.Heartbeat(ctx)
		switch {
		case err == nil:
			if resp.SyncIntervalSeconds > 0 {
				newInterval := time.Duration(resp.SyncIntervalSeconds) * time.Second
				if newInterval != interval {
					opts.Log.Info("sync interval updated",
						slog.String("from", interval.String()),
						slog.String("to", newInterval.String()))
					interval = newInterval
				}
			}
			if opts.Scheduler != nil {
				opts.Scheduler.Update(resp.ScreenSchedule)
			}
			if len(resp.Commands) > 0 {
				go dispatchCommands(ctx, opts, resp.Commands)
			}
			opts.Log.Debug("heartbeat ok",
				slog.Int("playlist_version", resp.PlaylistVersion),
				slog.String("update_channel", resp.UpdateChannel))
			if inFallback {
				opts.Log.Info("server recovered; restoring kiosk")
				if err := opts.Commander.SwitchURL(opts.KioskURL); err != nil {
					opts.Log.Warn("switch back to kiosk failed", slog.String("error", err.Error()))
				} else {
					inFallback = false
				}
			}

		case errors.Is(err, api.ErrUnauthorized):
			// Never substitute the offline image for an auth failure — main clears device_key.
			return err

		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err

		default:
			if inFallback {
				opts.Log.Warn("heartbeat failed (in fallback)", slog.String("error", err.Error()))
			} else if canFallback && isInitialHeartbeat && heartbeatConnectivityLoss(err) {
				inFallback = true
				opts.Log.Warn("panel unreachable on first heartbeat; switching to fallback",
					slog.String("error", err.Error()))
				if serr := opts.Commander.SwitchURL(opts.FallbackURL); serr != nil {
					opts.Log.Warn("switch to fallback failed", slog.String("error", serr.Error()))
				}
			} else {
				opts.Log.Warn("heartbeat failed", slog.String("error", err.Error()))
			}
		}

		firstHeartbeat = false

		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
