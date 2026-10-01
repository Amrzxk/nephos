package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/hook"
)

func TestHookCommand(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "hook.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan hook.Request, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hook.Request
		json.NewDecoder(r.Body).Decode(&request)
		got <- request
		w.WriteHeader(204)
	})}
	go server.Serve(ln)
	defer server.Close()
	state := map[string]any{"id": strings.Repeat("a", 64), "pid": 42, "annotations": map[string]string{"io.nephos.instance-id": "i-00000000000000001"}}
	raw, _ := json.Marshal(state)
	if err := run(context.Background(), strings.NewReader(string(raw)), socket); err != nil {
		t.Fatal(err)
	}
	if request := <-got; request.PID != 42 || request.ContainerID != strings.Repeat("a", 64) {
		t.Fatalf("request %+v", request)
	}
	if err := run(context.Background(), strings.NewReader("bad"), socket); err == nil {
		t.Fatal("malformed state succeeded")
	}
	if err := run(context.Background(), strings.NewReader(string(raw)), socket+".absent"); err == nil {
		t.Fatal("failed dial succeeded")
	}
}
