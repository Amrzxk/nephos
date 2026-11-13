package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Amrzxk/nephos/internal/version"
)

func TestHealthReportsDegradedWhileServing(t *testing.T) {
	tests := []struct {
		name            string
		ready, degraded bool
		wantCode        int
		wantStatus      string
	}{
		{"starting", false, false, http.StatusServiceUnavailable, "starting"},
		{"starting ignores degraded", false, true, http.StatusServiceUnavailable, "starting"},
		{"ready", true, false, http.StatusOK, "ready"},
		{"degraded", true, true, http.StatusOK, "degraded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New("secret", version.Get(), func() bool { return tt.ready }, nil, nil, nil, nil,
				WithDegraded(func() bool { return tt.degraded }))
			req := httptest.NewRequest(http.MethodGet, "/v1/health", http.NoBody)
			req.Host = "127.0.0.1:7788"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			var got struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tt.wantCode || got.Status != tt.wantStatus {
				t.Fatalf("got %d %q, want %d %q", rec.Code, got.Status, tt.wantCode, tt.wantStatus)
			}
		})
	}
}

func TestSessionLimitRejectsExtraSessions(t *testing.T) {
	limit := newSessionLimit(1)
	release, ok := limit.acquire(httptest.NewRecorder(), "console", "i-1")
	if !ok {
		t.Fatal("first session refused")
	}
	rec := httptest.NewRecorder()
	if _, ok := limit.acquire(rec, "console", "i-2"); ok {
		t.Fatal("second session admitted past the limit")
	}
	var body struct {
		Code       string `json:"code"`
		ResourceID string `json:"resource_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusTooManyRequests || body.Code != "RequestLimitExceeded" || body.ResourceID != "i-2" {
		t.Fatalf("over-limit response %d %+v", rec.Code, body)
	}
	release()
	if release, ok := limit.acquire(httptest.NewRecorder(), "console", "i-3"); !ok {
		t.Fatal("slot not returned after release")
	} else {
		release()
	}
}

func TestSessionLimitsHaveDefaultsAndOverrides(t *testing.T) {
	s := &server{consoleSessions: newSessionLimit(defaultMaxConsoleSessions), eventStreams: newSessionLimit(defaultMaxEventStreams)}
	WithSessionLimits(2, 0)(s)
	if cap(s.consoleSessions) != 2 || cap(s.eventStreams) != defaultMaxEventStreams {
		t.Fatalf("limits console=%d events=%d", cap(s.consoleSessions), cap(s.eventStreams))
	}
	WithSessionLimits(0, 3)(s)
	if cap(s.consoleSessions) != 2 || cap(s.eventStreams) != 3 {
		t.Fatalf("limits console=%d events=%d", cap(s.consoleSessions), cap(s.eventStreams))
	}
}
