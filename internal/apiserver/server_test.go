package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Amrzxk/nephos/internal/version"
)

func TestHealthIsPublicButVersionNeedsBearer(t *testing.T) {
	h := New("secret", version.Get(), func() bool { return true })
	for _, tc := range []struct {
		path, auth string
		want       int
	}{
		{"/v1/health", "", 200},
		{"/v1/version", "", 401},
		{"/v1/version", "Bearer wrong", 401},
		{"/v1/version", "Bearer secret", 200},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
		req.Host = "localhost:7788"
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s with auth %t: got %d want %d", tc.path, tc.auth != "", rec.Code, tc.want)
		}
	}
}

func TestHealthReportsStartingUntilReady(t *testing.T) {
	ready := false
	h := New("secret", version.Get(), func() bool { return ready })
	req := httptest.NewRequest(http.MethodGet, "/v1/health", http.NoBody)
	req.Host = "127.0.0.1:7788"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("starting status = %d", rec.Code)
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Status != "starting" {
		t.Fatalf("starting body = %q: %v", rec.Body.String(), err)
	}
	ready = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d", rec.Code)
	}
}

func TestRejectsUnexpectedHostAndBrowserOrigin(t *testing.T) {
	h := New("secret", version.Get(), func() bool { return true })
	for _, tc := range []struct {
		host, origin string
	}{
		{"attacker.example", ""},
		{"localhost:7788", "https://attacker.example"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/health", http.NoBody)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("host=%q origin=%q: status %d", tc.host, tc.origin, rec.Code)
		}
	}
}
