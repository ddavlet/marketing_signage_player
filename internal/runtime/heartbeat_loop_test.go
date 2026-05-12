package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marketing-signage/player/internal/api"
)

type recordingCommander struct {
	mu  sync.Mutex
	got []string
}

func (r *recordingCommander) KillCurrent() error { return nil }

func (r *recordingCommander) SwitchURL(u string) error {
	r.mu.Lock()
	r.got = append(r.got, u)
	r.mu.Unlock()
	return nil
}

func (r *recordingCommander) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.got))
	copy(out, r.got)
	return out
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHeartbeatFallback_5xxThenRecovers(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" || req.Method != http.MethodPost {
			http.NotFound(w, req)
			return
		}
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("overload"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cli, err := api.New(api.Options{
		BaseURL:   srv.URL,
		DeviceKey: func() string { return "device-key" },
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := &recordingCommander{}
	kiosk := "https://panel.example/player/abc/"
	fallback := "file:///tmp/fallback.html"

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	err = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 15 * time.Millisecond,
		KioskURL:        kiosk,
		FallbackURL:     fallback,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("heartbeatLoop: %v", err)
	}

	got := cmd.urls()
	if len(got) < 2 {
		t.Fatalf("expected fallback then kiosk SwitchURL, got %d calls: %v", len(got), got)
	}
	if got[0] != fallback {
		t.Errorf("first switch want fallback %q, got %q", fallback, got[0])
	}
	// After recovery, runtime restores kiosk (may repeat on later ok heartbeats only if inFallback was true once).
	foundKiosk := false
	for _, u := range got {
		if u == kiosk {
			foundKiosk = true
			break
		}
	}
	if !foundKiosk {
		t.Errorf("expected a SwitchURL to kiosk %q in %v", kiosk, got)
	}
}

func TestHeartbeatFallback_OnlyOneSwitchToFallbackWhileErrorsContinue(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		n++
		if n < 4 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("still bad"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cli, err := api.New(api.Options{
		BaseURL:   srv.URL,
		DeviceKey: func() string { return "k" },
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := &recordingCommander{}
	kiosk := "https://panel.example/player/x/"
	fallback := "file:///tmp/f.html"

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 20 * time.Millisecond,
		KioskURL:        kiosk,
		FallbackURL:     fallback,
	})

	got := cmd.urls()
	var fallbackCount int
	for _, u := range got {
		if u == fallback {
			fallbackCount++
		}
	}
	if fallbackCount != 1 {
		t.Errorf("want exactly 1 SwitchURL to fallback, got %d (all switches: %v)", fallbackCount, got)
	}
}

func TestHeartbeatMalformedJSON_NoFallbackSwitch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(srv.Close)

	cli, err := api.New(api.Options{
		BaseURL:   srv.URL,
		DeviceKey: func() string { return "k" },
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := &recordingCommander{}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	_ = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 25 * time.Millisecond,
		KioskURL:        "https://panel.example/player/x/",
		FallbackURL:     "file:///tmp/f.html",
	})

	if len(cmd.urls()) != 0 {
		t.Errorf("decode errors should not trigger fallback; got switches %v", cmd.urls())
	}
}

func TestHeartbeat404_TriggersFallback(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		n++
		if n == 1 {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cli, err := api.New(api.Options{
		BaseURL:   srv.URL,
		DeviceKey: func() string { return "k" },
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := &recordingCommander{}
	fallback := "file:///tmp/fb.html"
	kiosk := "https://panel.example/player/z/"

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	_ = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 15 * time.Millisecond,
		KioskURL:        kiosk,
		FallbackURL:     fallback,
	})

	got := cmd.urls()
	if len(got) == 0 || got[0] != fallback {
		t.Fatalf("expected first switch to fallback, got %v", got)
	}
}
