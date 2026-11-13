package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cannedResetAPI(t *testing.T, status int, body string) (cfg resourceConfig, query *string) {
	t.Helper()
	query = new(string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspaces/default/reset" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		*query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, testToken())}, query
}

func TestResetCLI(t *testing.T) {
	cleanBody := `{"workspace":"default","leak_check":{"complete":true,"clean":true,"findings":[],"inspection_errors":[]}}`
	leakBody := `{"code":"LeakDetected","message":"LeakDetected: 1 Nephos-owned objects remain after reset",` +
		`"leak_check":{"complete":true,"clean":false,"findings":[{"kind":"namespace","identity":"nx-v1","reason":"owned named namespace remains"}],"inspection_errors":[]}}`
	tests := []struct {
		name      string
		status    int
		body      string
		args      []string
		code      int
		query     string
		stdout    string
		stderr    string
		noRequest bool
	}{
		{name: "clean", status: 200, body: cleanBody, args: []string{"reset"}, code: 0, query: "timeout=300", stdout: "Workspace default is empty"},
		{name: "json", status: 200, body: cleanBody, args: []string{"reset", "-o", "json"}, code: 0, query: "timeout=300", stdout: `"clean":true`},
		{name: "custom timeout rounds up", status: 200, body: cleanBody, args: []string{"reset", "--timeout", "90500ms"}, code: 0, query: "timeout=91"},
		{name: "leaks", status: 409, body: leakBody, args: []string{"reset"}, code: 1, stderr: "namespace nx-v1: owned named namespace remains"},
		{name: "in progress", status: 409, body: `{"code":"ResetInProgress","message":"a workspace reset is in progress"}`, args: []string{"reset"}, code: 1, stderr: "ResetInProgress"},
		{name: "timeout", status: 504, body: `{"code":"ResetTimeout","message":"reset timed out with 1 instances"}`, args: []string{"reset"}, code: 1, stderr: "ResetTimeout"},
		{name: "zero timeout", args: []string{"reset", "--timeout", "0s"}, code: exitUsage, noRequest: true},
		{name: "too long", args: []string{"reset", "--timeout", "31m"}, code: exitUsage, noRequest: true},
		{name: "extra args", args: []string{"reset", "now"}, code: exitUsage, noRequest: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, query := cannedResetAPI(t, tt.status, tt.body)
			code, stdout, stderr := runCLIWithAPI(context.Background(), t, cfg, tt.args...)
			if code != tt.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if tt.noRequest && *query != "" {
				t.Fatalf("usage error still sent a request: %q", *query)
			}
			if tt.query != "" && *query != tt.query {
				t.Fatalf("query=%q want %q", *query, tt.query)
			}
			if !strings.Contains(stdout, tt.stdout) || !strings.Contains(stderr, tt.stderr) {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}
