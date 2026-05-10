package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/marketing-signage/player/internal/api"
	"github.com/marketing-signage/player/internal/config"
	"github.com/marketing-signage/player/internal/system"
)

// Pairer drives the first-boot registration handshake. It blocks until an
// admin approves the device in the control panel.
type Pairer struct {
	Client *api.Client
	Store  *config.Store
	Log    *slog.Logger

	// PendingInterval controls how often we re-poll while admin hasn't
	// approved yet. Defaults to 30s.
	PendingInterval time.Duration
}

// Wait returns nil when the device has a device_key (either already, or
// after admin approval). Returns ctx.Err() on cancellation.
func (p *Pairer) Wait(ctx context.Context) error {
	if p.Store.HasDeviceKey() {
		return nil
	}

	osInfo := system.ReadOSInfo()
	osInfoJSON, err := json.Marshal(osInfo)
	if err != nil {
		return fmt.Errorf("marshal os_info: %w", err)
	}
	req := api.RegisterRequest{
		HardwareID:    HardwareID(),
		Hostname:      Hostname(),
		OSInfo:        osInfoJSON,
		PlayerVersion: system.Version,
	}

	p.Log.Info("device unpaired; entering register loop",
		slog.String("hardware_id", req.HardwareID),
		slog.String("hostname", req.Hostname))

	pendingInterval := p.PendingInterval
	if pendingInterval <= 0 {
		pendingInterval = 30 * time.Second
	}
	errBackoff := newBackoff(15*time.Second, 5*time.Minute)

	for {
		resp, err := p.Client.Register(ctx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			wait := errBackoff.next()
			p.Log.Warn("register call failed",
				slog.String("error", err.Error()),
				slog.Duration("retry_in", wait))
			if serr := sleep(ctx, wait); serr != nil {
				return serr
			}
			continue
		}
		errBackoff.reset()

		switch resp.Status {
		case api.RegisterStatusApproved:
			if resp.DeviceKey == "" {
				return errors.New("server returned approved without device_key")
			}
			if err := p.Store.SetDeviceKey(resp.DeviceKey); err != nil {
				return fmt.Errorf("persist device_key: %w", err)
			}
			p.Log.Info("device approved and paired")
			return nil

		case api.RegisterStatusPending:
			wait := jitter(pendingInterval, 0.2)
			p.Log.Info("device pending admin approval",
				slog.Duration("retry_in", wait))
			if err := sleep(ctx, wait); err != nil {
				return err
			}

		default:
			wait := errBackoff.next()
			p.Log.Warn("unexpected register status",
				slog.String("status", resp.Status),
				slog.Duration("retry_in", wait))
			if err := sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func jitter(d time.Duration, frac float64) time.Duration {
	if d <= 0 {
		return d
	}
	span := float64(d) * frac
	delta := time.Duration((rand.Float64()*2 - 1) * span)
	return d + delta
}

type backoff struct {
	initial, cur, max time.Duration
}

func newBackoff(initial, max time.Duration) *backoff {
	return &backoff{initial: initial, cur: initial, max: max}
}

func (b *backoff) next() time.Duration {
	d := b.cur
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
	return jitter(d, 0.25)
}

func (b *backoff) reset() { b.cur = b.initial }
