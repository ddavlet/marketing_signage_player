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

// failingCommander fails SwitchURL for the first failCount calls, then succeeds.
type failingCommander struct {
	recordingCommander
	mu        sync.Mutex
	failCount int
}

func (f *failingCommander) SwitchURL(u string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordingCommander.mu.Lock()
	f.recordingCommander.got = append(f.recordingCommander.got, u)
	f.recordingCommander.mu.Unlock()
	if f.failCount > 0 {
		f.failCount--
		return errors.New("switch failed")
	}
	return nil
}

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

func TestHeartbeatAfterSuccess_DoesNotSwitchToFallbackOn5xx(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("down"))
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
	fallback := "file:///tmp/never-use.html"

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_ = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 20 * time.Millisecond,
		KioskURL:        "https://panel.example/player/x/",
		FallbackURL:     fallback,
	})

	for _, u := range cmd.urls() {
		if u == fallback {
			t.Fatalf("after a successful heartbeat, 5xx must not switch to fallback; got %v", cmd.urls())
		}
	}
}

func TestHeartbeat401_ReturnsUnauthorizedWithoutFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
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
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 20 * time.Millisecond,
		KioskURL:        "https://panel.example/player/x/",
		FallbackURL:     "file:///tmp/fallback.html",
	})
	if !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if len(cmd.urls()) != 0 {
		t.Fatalf("401 must not use heartbeat fallback; got %v", cmd.urls())
	}
}

// If the first heartbeat fails for a non-connectivity reason, a later 503 must
// not trigger fallback (only the first attempt may).
func TestHeartbeatAfterDecodeErrorSecond503_NoFallback(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		n++
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`not-json`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
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
	fallback := "file:///tmp/should-not-appear.html"

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_ = heartbeatLoop(ctx, Options{
		Client:          cli,
		Commander:       cmd,
		Log:             quietLog(),
		DefaultInterval: 20 * time.Millisecond,
		KioskURL:        "https://panel.example/player/x/",
		FallbackURL:     fallback,
	})

	for _, u := range cmd.urls() {
		if u == fallback {
			t.Fatalf("503 on second heartbeat must not open fallback after non-connectivity first error; got %v", cmd.urls())
		}
	}
}

// If SwitchURL(kiosk) fails during fallback recovery, inFallback must stay
// true so the next successful heartbeat retries the switch.
func TestHeartbeatRecovery_RetriesKioskSwitchIfFirstFails(t *testing.T) {
	t.Parallel()

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/device/heartbeat/" {
			http.NotFound(w, req)
			return
		}
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("down"))
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

	// failCount: 2 — first call is SwitchURL(fallback), second is SwitchURL(kiosk)
	// recovery attempt. Both fail; third call (kiosk retry) must succeed.
	cmd := &failingCommander{failCount: 2}
	kiosk := "https://panel.example/player/x/"
	fallback := "file:///tmp/fb.html"

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
	// Expected sequence: fallback, kiosk (failed), kiosk (succeeded), ...
	// At minimum we expect the kiosk URL to appear at least twice.
	var kioskCount int
	for _, u := range got {
		if u == kiosk {
			kioskCount++
		}
	}
	if kioskCount < 2 {
		t.Errorf("expected kiosk SwitchURL to be retried (at least 2 calls), got %d; all: %v", kioskCount, got)
	}
}
