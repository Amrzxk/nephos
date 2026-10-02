package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Plumb sends a verified runtime identity over the private Unix socket.
func Plumb(ctx context.Context, socket string, request Request) error {
	if err := request.validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(socket) || strings.Contains(socket, "://") {
		return fmt.Errorf("hook requires an absolute Unix socket path")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode hook request: %w", err)
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("hook redirects are forbidden") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://nephos/plumb", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call private hook socket: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("plumbing refused (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}
