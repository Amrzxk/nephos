package appliance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHealthProbeStates(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want HealthState
	}{
		{"ready", http.StatusOK, `{"status":"ready"}`, HealthReady},
		{"degraded", http.StatusOK, `{"status":"degraded"}`, HealthDegraded},
		{"starting", http.StatusServiceUnavailable, `{"status":"starting"}`, HealthStarting},
		{"mismatched code", http.StatusServiceUnavailable, `{"status":"ready"}`, HealthUnhealthy},
		{"garbage", http.StatusOK, `not json`, HealthUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			got, err := HTTPHealthProbe{Client: srv.Client(), URL: srv.URL}.Check(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("Check = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
