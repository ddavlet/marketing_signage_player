package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientDo_TransportErrorIsErrTransport(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// never reached — client hits closed listener
	}))
	srv.Close()

	cli, err := New(Options{BaseURL: srv.URL, DeviceKey: func() string { return "k" }})
	if err != nil {
		t.Fatal(err)
	}
	var out HeartbeatResponse
	err = cli.do(context.Background(), http.MethodPost, "/api/device/heartbeat/", struct{}{}, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("errors.Is(err, ErrTransport) = false; err=%v", err)
	}
}
