package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

type stubVerifier struct {
	result model.LeakResult
	err    error
}

func (v stubVerifier) Verify(context.Context, string) (model.LeakResult, error) {
	return v.result, v.err
}

func resetAPI(t *testing.T, verifier service.ResetVerifier, ready bool) (http.Handler, *service.ResetGuard) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	network := service.NewNetwork(s, nil, nil)
	instances := service.NewInstances(s, nil, nil)
	guard := &service.ResetGuard{}
	network.GuardResets(guard)
	instances.GuardResets(guard)
	reset := service.NewReset(s, network, instances, guard, verifier)
	return New("secret", version.Get(), func() bool { return ready }, network, instances, s, nil, WithReset(reset)), guard
}

func TestResetWorkspaceStatusAndBody(t *testing.T) {
	cleanResult := model.LeakResult{Complete: true, Clean: true}
	residue := model.LeakResult{Complete: true, Findings: []model.LeakFinding{{Kind: "runtime", Identity: "abc", Reason: "marked runtime root remains"}}}
	tests := []struct {
		name     string
		verifier stubVerifier
		ready    bool
		path     string
		status   int
		code     string
	}{
		{name: "clean", verifier: stubVerifier{result: cleanResult}, ready: true, path: "/v1/workspaces/default/reset", status: 200},
		{name: "leaks", verifier: stubVerifier{result: residue}, ready: true, path: "/v1/workspaces/default/reset", status: 409, code: "LeakDetected"},
		{name: "verify error", verifier: stubVerifier{err: errors.New("boom")}, ready: true, path: "/v1/workspaces/default/reset", status: 500, code: "InternalError"},
		{name: "not ready", verifier: stubVerifier{result: cleanResult}, ready: false, path: "/v1/workspaces/default/reset", status: 503, code: "ServiceUnavailable"},
		{name: "bad timeout", verifier: stubVerifier{result: cleanResult}, ready: true, path: "/v1/workspaces/default/reset?timeout=0", status: 400, code: "InvalidParameterValue"},
		{name: "huge timeout", verifier: stubVerifier{result: cleanResult}, ready: true, path: "/v1/workspaces/default/reset?timeout=1801", status: 400, code: "InvalidParameterValue"},
		{name: "other workspace", verifier: stubVerifier{result: cleanResult}, ready: true, path: "/v1/workspaces/lab/reset", status: 404, code: "InvalidWorkspace.NotFound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := resetAPI(t, tt.verifier, tt.ready)
			rec := apiRequest(h, http.MethodPost, tt.path, "", "")
			if rec.Code != tt.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			body := readObject(t, rec)
			if tt.code != "" && body["code"] != tt.code {
				t.Fatalf("code=%v body=%v", body["code"], body)
			}
			if tt.status == 200 {
				check, _ := body["leak_check"].(map[string]any)
				if body["workspace"] != "default" || check["clean"] != true || check["findings"] == nil || check["inspection_errors"] == nil {
					t.Fatalf("body=%v", body)
				}
			}
			if tt.code == "LeakDetected" {
				check, _ := body["leak_check"].(map[string]any)
				if findings, _ := check["findings"].([]any); len(findings) != 1 {
					t.Fatalf("leak_check=%v", check)
				}
			}
		})
	}
}

func TestResetRequiresBearer(t *testing.T) {
	h, _ := resetAPI(t, stubVerifier{result: model.LeakResult{Complete: true, Clean: true}}, true)
	req := apiRequest(h, http.MethodPost, "/v1/workspaces/default/reset", "", "")
	if req.Code != 200 {
		t.Fatalf("authenticated status=%d", req.Code)
	}
	h2, _ := resetAPI(t, stubVerifier{}, true)
	unauth := httptest.NewRequest(http.MethodPost, "/v1/workspaces/default/reset", http.NoBody)
	unauth.Host = "localhost:7788"
	rec := httptest.NewRecorder()
	h2.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rec.Code)
	}
}

func TestCreateDuringResetReturnsResetInProgress(t *testing.T) {
	h, guard := resetAPI(t, stubVerifier{result: model.LeakResult{Complete: true, Clean: true}}, true)
	held := blockingStore{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.NewReset(held, nil, nil, guard, nil).Run(context.Background())
	}()
	<-held.entered // Run takes the guard before its first snapshot.
	rec := apiRequest(h, http.MethodPost, "/v1/workspaces/default/vpcs", `{"name":"late","cidr_block":"10.0.0.0/16"}`, "")
	if rec.Code != http.StatusConflict || readObject(t, rec)["code"] != "ResetInProgress" {
		t.Fatalf("create during reset: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodPost, "/v1/workspaces/default/reset", "", "")
	if rec.Code != http.StatusConflict || readObject(t, rec)["code"] != "ResetInProgress" {
		t.Fatalf("concurrent reset: %d %s", rec.Code, rec.Body.String())
	}
	close(held.release)
	<-done
}

// blockingStore parks a reset inside its first snapshot.
type blockingStore struct{ entered, release chan struct{} }

func (b blockingStore) DesiredSnapshot(context.Context, string) (store.DesiredState, error) {
	close(b.entered)
	<-b.release
	return store.DesiredState{}, errors.New("released")
}
