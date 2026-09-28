package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/version"
)

func TestServeCreatesTokenAndShutsDown(t *testing.T) {
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets", "api-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, path, version.Get()) }()

	client := &http.Client{Timeout: time.Second}
	url := "http://" + ln.Addr().String() + "/v1/health"
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never became ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) < 43 {
		t.Fatalf("token not created: length=%d err=%v", len(data), err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop on cancellation")
	}
}
