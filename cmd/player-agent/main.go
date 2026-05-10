// Command player-agent is the headless background service that runs on
// signage devices. It supervises Chromium kiosk, registers/heartbeats with
// the control panel, and self-updates from a release feed.
//
// All content-side concerns (playlist sync, media playback, schedule
// evaluation, offline caching) live in the browser at /player/<uuid>/.
// The agent only owns OS-level concerns.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/marketing-signage/player/internal/api"
	"github.com/marketing-signage/player/internal/config"
	"github.com/marketing-signage/player/internal/identity"
	"github.com/marketing-signage/player/internal/logging"
	"github.com/marketing-signage/player/internal/runtime"
	"github.com/marketing-signage/player/internal/scheduler"
	"github.com/marketing-signage/player/internal/supervisor"
	"github.com/marketing-signage/player/internal/system"
	"github.com/marketing-signage/player/internal/updater"
)

func main() {
	configPath := flag.String("config", config.DefaultPath, "path to config.toml")
	printHWID := flag.Bool("print-hwid", false, "print hardware ID and exit")
	flag.Parse()

	if *printHWID {
		fmt.Println(identity.HardwareID())
		return
	}

	if err := run(*configPath); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	store, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := store.Get()
	if cfg.ServerURL == "" {
		return errors.New("server_url is empty; set it in " + configPath)
	}

	log := logging.New(cfg.LogLevel)

	osInfo := system.ReadOSInfo()
	osInfoJSON, _ := json.Marshal(osInfo)
	log.Info("marketing-signage-player starting",
		slog.String("version", system.Version),
		slog.String("server_url", cfg.ServerURL),
		slog.Bool("has_device_key", store.HasDeviceKey()),
		slog.String("update_channel", cfg.UpdateChannel),
		slog.String("hardware_id", identity.HardwareID()),
		slog.String("hostname", identity.Hostname()),
		slog.String("os_info", string(osInfoJSON)),
	)

	client, err := api.New(api.Options{
		BaseURL:   cfg.ServerURL,
		DeviceKey: store.DeviceKey,
		Version:   system.Version,
	})
	if err != nil {
		return fmt.Errorf("build api client: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pairer := &identity.Pairer{
		Client: client,
		Store:  store,
		Log:    log.With(slog.String("subsystem", "identity")),
	}

	for {
		if err := pairer.Wait(ctx); err != nil {
			return fmt.Errorf("pairing: %w", err)
		}

		sv := buildSupervisor(cfg, store.DeviceKey(), log)
		sched := scheduler.New(log)
		upd := updater.New(updater.Options{
			Releases: client,
			Channel:  func() string { return store.Get().UpdateChannel },
			Log:      log,
		})

		// Avoid wrapping a nil *Supervisor in a non-nil interface.
		var rtSupervisor runtime.Subsystem
		var cmdr runtime.Commander
		if sv != nil {
			rtSupervisor = sv
			cmdr = sv
		}

		err := runtime.Run(ctx, runtime.Options{
			Client:     client,
			Supervisor: rtSupervisor,
			Commander:  cmdr,
			Scheduler:  sched,
			Updater:    upd,
			Log:        log.With(slog.String("subsystem", "runtime")),
		})
		if errors.Is(err, api.ErrUnauthorized) {
			log.Warn("server returned unauthorized; clearing device_key and re-pairing")
			if cerr := store.ClearDeviceKey(); cerr != nil {
				return fmt.Errorf("clear device_key: %w", cerr)
			}
			continue
		}
		return err
	}
}

// buildSupervisor returns a Chromium supervisor pointed at /player/<key>/,
// or nil if no chromium binary is available on this host. The agent stays
// useful as a heartbeat-only daemon in that case (e.g. dev workflow).
func buildSupervisor(cfg config.Snapshot, deviceKey string, log *slog.Logger) *supervisor.Supervisor {
	kioskURL := strings.TrimRight(cfg.ServerURL, "/") + "/player/" + deviceKey + "/"
	dataDir := filepath.Join(cfg.DataDir, "chromium")

	sv, err := supervisor.New(supervisor.Options{
		BinaryPath:  cfg.ChromiumPath, // empty triggers auto-detect
		KioskURL:    kioskURL,
		UserDataDir: dataDir,
		Log:         log.With(slog.String("subsystem", "supervisor")),
	})
	if err != nil {
		log.Warn("chromium supervisor disabled — running heartbeat only",
			slog.String("error", err.Error()))
		return nil
	}
	log.Info("chromium supervisor ready",
		slog.String("kiosk_url", kioskURL),
		slog.String("user_data_dir", dataDir))
	return sv
}
