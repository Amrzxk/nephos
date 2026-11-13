// Package podman implements the small libpod 5.4.2 API surface used by M1.
// Its only transport is the appliance-private Unix socket.
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
)

const apiPath = "/v5.4.2/libpod"

// Client speaks only to the appliance-private Podman Unix socket.
type Client struct {
	socket string
	http   *http.Client
}

var _ compute.Runtime = (*Client)(nil)

// New constructs a Podman client without a TCP fallback.
func New(socketPath string) *Client {
	c := &Client{socket: socketPath}
	c.http = &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return c.dial(ctx) },
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("podman redirects are forbidden") }}
	return c
}
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if !filepath.IsAbs(c.socket) || strings.Contains(c.socket, "://") {
		return nil, fmt.Errorf("podman requires an absolute Unix socket path")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("dial private Podman socket: %w", err)
	}
	return conn, nil
}

// APIError retains the HTTP context of a rejected local runtime request.
type APIError struct {
	Status               int
	Method, Path, Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Podman %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Detail)
}
func isStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}
func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mutation := method != http.MethodGet && method != http.MethodHead
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Podman request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://podman"+apiPath+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var connected atomic.Bool
	// Without an assigned connection no byte could have been transmitted.
	// Once assigned, a partial write/lost reply remains conservatively unknown.
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := c.http.Do(req)
	if err != nil {
		if mutation && connected.Load() {
			err = errors.Join(err, compute.ErrUncertainEffect)
		} else if !mutation {
			err = errors.Join(err, compute.ErrInspectionUnavailable)
		}
		return fmt.Errorf("podman %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var failure error = &APIError{Status: resp.StatusCode, Method: method, Path: path, Detail: strings.TrimSpace(string(detail))}
		if !mutation && resp.StatusCode >= 500 {
			failure = errors.Join(failure, compute.ErrInspectionUnavailable)
		}
		return failure
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			if mutation {
				err = errors.Join(err, compute.ErrUncertainEffect)
			} else {
				err = errors.Join(err, compute.ErrInspectionUnavailable)
			}
			return fmt.Errorf("decode Podman %s: %w", path, err)
		}
	}
	return nil
}
