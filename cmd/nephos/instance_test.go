package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

func TestInstanceCLI(t *testing.T) {
	ctx := context.Background()
	cfg := realResourceAPI(t)
	for _, args := range [][]string{
		{"vpc", "create", "VPC", "--cidr-block", "10.0.0.0/16"},
		{"subnet", "create", "Subnet α", "--vpc", "VPC", "--cidr-block", "10.0.1.0/24", "--availability-zone", "local-1a"},
	} {
		if code, _, errOut := runCLIWithAPI(ctx, t, cfg, args...); code != 0 {
			t.Fatal(errOut)
		}
	}
	code, out, errOut := runCLIWithAPI(ctx, t, cfg, "instance", "run", "Server α", "--subnet", "Subnet α", "-o", "json")
	if code != 0 || errOut != "" {
		t.Fatalf("run code=%d out=%q err=%q", code, out, errOut)
	}
	var instance client.Instance
	if err := json.Unmarshal([]byte(out), &instance); err != nil {
		t.Fatal(err)
	}
	if instance.Name != "Server α" || instance.PrivateIp != "10.0.1.4" || instance.State != client.InstanceStatePending {
		t.Fatalf("instance %+v", instance)
	}
	for _, ref := range []string{instance.Id, instance.Name} {
		code, got, errOut := runCLIWithAPI(ctx, t, cfg, "instance", "describe", ref, "-o", "json")
		if code != 0 || got != out || errOut != "" {
			t.Fatalf("describe %q: %d %q %q", ref, code, got, errOut)
		}
	}
	if code, _, _ := runCLIWithAPI(ctx, t, cfg, "instance", "describe", "server α"); code == 0 {
		t.Fatal("case-insensitive lookup")
	}
	if code, listed, errOut := runCLIWithAPI(ctx, t, cfg, "instance", "list", "--limit", "1", "-o", "json"); code != 0 || !strings.Contains(listed, instance.Id) {
		t.Fatalf("list %d %q", code, errOut)
	}
	if code, terminated, errOut := runCLIWithAPI(ctx, t, cfg, "instance", "terminate", instance.Name, "-o", "json"); code != 0 || !strings.Contains(terminated, "shutting-down") {
		t.Fatalf("terminate %d %q %q", code, terminated, errOut)
	}
}

func TestInstanceCLIReplayAndPagedLookup(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/subnets/subnet-00000000000000001"):
			fmt.Fprint(w, `{"id":"subnet-00000000000000001"}`)
		case r.Method == http.MethodPost:
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if len(keys) == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":"i-00000000000000001","name":"Server α","state":"pending"}`)
		case strings.HasSuffix(r.URL.Path, "/instances"):
			if r.URL.Query().Get("page_token") == "" {
				fmt.Fprint(w, `{"items":[],"next_page_token":"next"}`)
			} else {
				fmt.Fprint(w, `{"items":[{"id":"i-00000000000000001","name":"Server α"}]}`)
			}
		default:
			fmt.Fprint(w, `{"id":"i-00000000000000001","name":"Server α","state":"running"}`)
		}
	}))
	defer srv.Close()
	cfg := resourceConfig{endpoint: srv.URL, credentialPath: tokenFile(t, testToken())}
	if code, _, errOut := runCLIWithAPI(context.Background(), t, cfg, "instance", "run", "Server α", "--subnet", "subnet-00000000000000001"); code != 0 {
		t.Fatal(errOut)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("retry keys=%v", keys)
	}
	if code, out, errOut := runCLIWithAPI(context.Background(), t, cfg, "instance", "describe", "Server α", "-o", "json"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("paged lookup %d %q %q", code, out, errOut)
	}
}

func TestInstanceWait(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		deleting, cancel bool
	}{
		{"running", `{"id":"i-1","state":"running"}`, "", 200, false, false},
		{"failed", `{"id":"i-1","state":"failed","state_reason":"hook failed"}`, "hook failed", 200, false, false},
		{"timeout", `{"id":"i-1","state":"pending"}`, "timed out", 200, false, false},
		{"deleted", `{}`, "", 404, true, false},
		{"canceled", `{}`, "canceled", 200, false, true},
		{"unauthorized", `{"code":"Unauthorized","message":"invalid credential"}`, "Unauthorized", 401, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			api, _, err := newResourceClient(resourceConfig{endpoint: srv.URL, credentialPath: tokenFile(t, testToken())})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			_, err = waitInstance(ctx, api, "i-1", tc.deleting, 20*time.Millisecond, time.Millisecond)
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), testToken())) {
				t.Fatalf("wait err=%v", err)
			}
		})
	}
}
