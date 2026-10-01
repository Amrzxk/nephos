package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/apiserver"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testToken() string { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }

func runCLIWithAPI(ctx context.Context, t *testing.T, cfg resourceConfig, args ...string) (exitCode int, output, errorOutput string) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()
	code := runWithConfig(ctx, args, out, errFile, cfg)
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := errFile.Sync(); err != nil {
		t.Fatal(err)
	}
	outBytes, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	errBytes, err := os.ReadFile(errFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(outBytes), string(errBytes)
}

func realResourceAPI(t *testing.T) resourceConfig {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	token := testToken()
	server := httptest.NewServer(apiserver.New(token, version.Get(), func() bool { return true }, service.NewNetwork(s, nil, nil), service.NewInstances(s, nil, nil), s, nil))
	t.Cleanup(server.Close)
	return resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, token), pollInterval: time.Millisecond}
}

func TestResourceCLIUsesGeneratedAPIAndCaseSensitiveNames(t *testing.T) {
	cfg := realResourceAPI(t)
	ctx := context.Background()
	code, out, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "create", "Lab East α", "--cidr-block", "10.0.1.9/16", "-o", "json")
	if code != 0 || errOut != "" {
		t.Fatalf("create VPC code=%d out=%q err=%q", code, out, errOut)
	}
	var created struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		CIDRBlock string `json:"cidr_block"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "vpc-") || created.Name != "Lab East α" || created.CIDRBlock != "10.0.0.0/16" || created.State != "pending" {
		t.Fatalf("created=%+v", created)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "describe", "Lab East α")
	if code != 0 || !strings.Contains(out, created.ID) || errOut != "" {
		t.Fatalf("describe by name code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "describe", created.ID, "-o", "json")
	if code != 0 || !strings.Contains(out, `"name":"Lab East α"`) || errOut != "" {
		t.Fatalf("describe by ID code=%d out=%q err=%q", code, out, errOut)
	}
	code, _, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "describe", "lab east α")
	if code == 0 || !strings.Contains(errOut, "not found") {
		t.Fatalf("case-insensitive lookup unexpectedly succeeded: %q", errOut)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "subnet", "create", "App α", "--vpc", "Lab East α", "--cidr-block", "10.0.1.0/24", "--availability-zone", "local-1a", "-o", "json")
	if code != 0 || errOut != "" {
		t.Fatalf("create subnet code=%d out=%q err=%q", code, out, errOut)
	}
	var subnet struct {
		ID    string `json:"id"`
		VPCID string `json:"vpc_id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &subnet); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(subnet.ID, "subnet-") || subnet.VPCID != created.ID || subnet.Name != "App α" {
		t.Fatalf("created subnet=%+v", subnet)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "subnet", "describe", "App α", "-o", "json")
	if code != 0 || !strings.Contains(out, subnet.ID) || errOut != "" {
		t.Fatalf("subnet describe code=%d out=%q err=%q", code, out, errOut)
	}
	code, _, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "delete", created.ID)
	if code == 0 || !strings.Contains(errOut, "DependencyViolation") {
		t.Fatalf("dependent delete code=%d err=%q", code, errOut)
	}
}

func TestResourceCLIListPaginationAndJSON(t *testing.T) {
	cfg := realResourceAPI(t)
	ctx := context.Background()
	for _, name := range []string{"East", "West"} {
		if code, _, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "create", name, "--cidr-block", "10.0.0.0/16"); code != 0 {
			t.Fatalf("create %s: %s", name, errOut)
		}
	}
	code, out, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "list", "--limit", "1", "-o", "json")
	if code != 0 || errOut != "" {
		t.Fatalf("list code=%d out=%q err=%q", code, out, errOut)
	}
	var first struct {
		Items []map[string]any `json:"items"`
		Next  string           `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Next == "" {
		t.Fatalf("page=%+v", first)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "list", "--limit", "1", "--page-token", first.Next, "-o", "json")
	if code != 0 || errOut != "" {
		t.Fatalf("next page code=%d out=%q err=%q", code, out, errOut)
	}
	var second struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0]["id"] == first.Items[0]["id"] {
		t.Fatalf("next page=%+v", second)
	}
}

func TestCreateRetryReusesOneIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	keys := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/default/vpcs" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		attempt := len(keys)
		mu.Unlock()
		if attempt == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":"pending","state_reason":"","generation":1,"observed_generation":0}`))
	}))
	defer server.Close()
	cfg := resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, testToken()), pollInterval: time.Millisecond}
	code, out, errOut := runCLIWithAPI(context.Background(), t, cfg, "vpc", "create", "East", "--cidr-block", "10.0.0.0/16")
	mu.Lock()
	got := append([]string(nil), keys...)
	mu.Unlock()
	if code != 0 || !strings.Contains(out, "vpc-0123456789abcdef0") || errOut != "" || len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("retry code=%d out=%q err=%q keys=%v", code, out, errOut, got)
	}
}

func TestResourceCLIAuthenticationTimeoutAndCancellation(t *testing.T) {
	token := testToken()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"code":"AuthFailure","message":"bad token"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":"pending","state_reason":"","generation":1,"observed_generation":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":"pending","state_reason":"","generation":1,"observed_generation":0}`))
	}))
	defer server.Close()
	cfg := resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, token), pollInterval: time.Millisecond}
	code, _, errOut := runCLIWithAPI(context.Background(), t, cfg, "vpc", "create", "East", "--cidr-block", "10.0.0.0/16", "--wait", "--timeout", "15ms")
	if code == 0 || !strings.Contains(errOut, "timed out") {
		t.Fatalf("wait timeout code=%d err=%q", code, errOut)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	code, _, errOut = runCLIWithAPI(canceled, t, cfg, "vpc", "list")
	if code == 0 || errOut == "" {
		t.Fatalf("canceled command code=%d err=%q", code, errOut)
	}
	badCfg := cfg
	badCfg.credentialPath = tokenFile(t, base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	code, _, errOut = runCLIWithAPI(context.Background(), t, badCfg, "vpc", "list")
	if code == 0 || !strings.Contains(errOut, "AuthFailure") || strings.Contains(errOut, token) {
		t.Fatalf("auth code=%d err=%q", code, errOut)
	}
}

func TestWaitReportsAvailableAndTerminalFailure(t *testing.T) {
	for _, tc := range []struct {
		name, final, reason string
		wantCode            int
	}{
		{name: "available", final: "available", wantCode: 0},
		{name: "failed", final: "failed", reason: "gateway add: operation not permitted", wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				state, reason := "pending", ""
				if r.Method == http.MethodGet {
					if gets.Add(1) >= 2 {
						state, reason = tc.final, tc.reason
					}
				} else {
					w.WriteHeader(http.StatusCreated)
				}
				_, _ = fmt.Fprintf(w, `{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":%q,"state_reason":%q,"generation":1,"observed_generation":1}`, state, reason)
			}))
			defer server.Close()
			cfg := resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, testToken()), pollInterval: time.Millisecond}
			code, out, errOut := runCLIWithAPI(context.Background(), t, cfg, "vpc", "create", "East", "--cidr-block", "10.0.0.0/16", "--wait")
			if code != tc.wantCode || gets.Load() < 2 {
				t.Fatalf("code=%d gets=%d out=%q err=%q", code, gets.Load(), out, errOut)
			}
			if tc.wantCode == 0 && (!strings.Contains(out, "available") || errOut != "") {
				t.Fatalf("available out=%q err=%q", out, errOut)
			}
			if tc.wantCode != 0 && (!strings.Contains(errOut, tc.reason) || out != "") {
				t.Fatalf("failed out=%q err=%q", out, errOut)
			}
		})
	}
}

func TestNameLookupCrossesGeneratedClientPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/workspaces/default/vpcs" {
			if r.URL.Query().Get("page_token") == "next" {
				_, _ = w.Write([]byte(`{"items":[{"id":"vpc-0123456789abcdef0","name":"Target","cidr_block":"10.0.0.0/16","state":"available","state_reason":"","generation":1,"observed_generation":1}]}`))
			} else {
				_, _ = w.Write([]byte(`{"items":[{"id":"vpc-fffffffffffffffff","name":"Other","cidr_block":"10.0.0.0/16","state":"available","state_reason":"","generation":1,"observed_generation":1}],"next_page_token":"next"}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"Target","cidr_block":"10.0.0.0/16","state":"available","state_reason":"","generation":1,"observed_generation":1}`))
	}))
	defer server.Close()
	cfg := resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, testToken()), pollInterval: time.Millisecond}
	code, out, errOut := runCLIWithAPI(context.Background(), t, cfg, "vpc", "describe", "Target", "-o", "json")
	if code != 0 || !strings.Contains(out, "vpc-0123456789abcdef0") || errOut != "" {
		t.Fatalf("paged name lookup code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestIDShapedNameFallsBackAfterMissingID(t *testing.T) {
	cfg := realResourceAPI(t)
	ctx := context.Background()
	name := "vpc-aaaaaaaaaaaaaaaaa"
	code, out, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "create", name, "--cidr-block", "10.0.0.0/16", "-o", "json")
	if code != 0 {
		t.Fatalf("create code=%d out=%q err=%q", code, out, errOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runCLIWithAPI(ctx, t, cfg, "vpc", "describe", name, "-o", "json")
	if code != 0 || !strings.Contains(out, created.ID) || errOut != "" {
		t.Fatalf("ID-shaped name code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestDeleteWaitReportsCompletedRemoval(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			deleted.Store(true)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":"deleting","state_reason":"","generation":2,"observed_generation":1}`))
			return
		}
		if deleted.Load() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"InvalidVpcID.NotFound","message":"gone"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"vpc-0123456789abcdef0","name":"East","cidr_block":"10.0.0.0/16","state":"available","state_reason":"","generation":1,"observed_generation":1}`))
	}))
	defer server.Close()
	cfg := resourceConfig{endpoint: server.URL, credentialPath: tokenFile(t, testToken()), pollInterval: time.Millisecond}
	code, out, errOut := runCLIWithAPI(context.Background(), t, cfg, "vpc", "delete", "vpc-0123456789abcdef0", "--wait")
	if code != 0 || !strings.Contains(out, "deleted") || strings.Contains(out, "deleting") || errOut != "" {
		t.Fatalf("delete wait code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestCLIRejectsExplicitZeroPageLimitAndAllowsDashName(t *testing.T) {
	cfg := realResourceAPI(t)
	ctx := context.Background()
	code, _, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "list", "--limit", "0")
	if code != exitUsage || !strings.Contains(errOut, "--limit") {
		t.Fatalf("zero limit code=%d err=%q", code, errOut)
	}
	code, out, errOut := runCLIWithAPI(ctx, t, cfg, "vpc", "create", "--", "-Lab", "--cidr-block", "10.0.0.0/16", "-o", "json")
	if code != 0 || !strings.Contains(out, `"name":"-Lab"`) || errOut != "" {
		t.Fatalf("dash name code=%d out=%q err=%q", code, out, errOut)
	}
}
