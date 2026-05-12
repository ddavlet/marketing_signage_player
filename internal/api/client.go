package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrNotFound     = errors.New("not found")
	ErrHTTPError    = errors.New("http error") // wraps any 4xx/5xx not handled specifically
	// ErrTransport marks failures from http.Client.Do (no TCP response, TLS, timeouts, etc.).
	ErrTransport = errors.New("transport error")
)

// KeyProvider is a function so the client can read the latest device key
// after registration without being rebuilt.
type KeyProvider func() string

type Options struct {
	BaseURL   string
	DeviceKey KeyProvider
	Version   string
	Timeout   time.Duration
	SSHPort   int
}

type Client struct {
	base      *url.URL
	http      *http.Client
	deviceKey KeyProvider
	userAgent string
	version   string
	sshPort   int
}

func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("base url required")
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Client{
		base:      base,
		http:      &http.Client{Timeout: opts.Timeout},
		deviceKey: opts.DeviceKey,
		userAgent: "marketing-signage-player/" + opts.Version,
		version:   opts.Version,
		sshPort:   opts.SSHPort,
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("parse path: %w", err)
	}
	u := c.base.ResolveReference(ref)

	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Player-Version", c.version)
	req.Header.Set("Accept", "application/json")
	if c.sshPort > 0 {
		req.Header.Set("X-SSH-Port", strconv.Itoa(c.sshPort))
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.deviceKey != nil {
		if k := c.deviceKey(); k != "" {
			req.Header.Set("X-Device-Key", k)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, errors.Join(ErrTransport, err))
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 400:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: status %d: %s: %w", method, path, resp.StatusCode, string(b), ErrHTTPError)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	var out RegisterResponse
	if err := c.do(ctx, http.MethodPost, "/api/device/register/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Heartbeat(ctx context.Context) (*HeartbeatResponse, error) {
	var out HeartbeatResponse
	if err := c.do(ctx, http.MethodPost, "/api/device/heartbeat/", struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) AckCommand(ctx context.Context, commandID int) error {
	path := fmt.Sprintf("/api/device/commands/%d/ack/", commandID)
	return c.do(ctx, http.MethodPost, path, struct{}{}, nil)
}

func (c *Client) LatestRelease(ctx context.Context, channel, os, arch string) (*Release, error) {
	q := url.Values{}
	q.Set("channel", channel)
	q.Set("os", os)
	q.Set("arch", arch)
	var out Release
	if err := c.do(ctx, http.MethodGet, "/api/player/releases/latest?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
