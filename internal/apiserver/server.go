// Package apiserver serves the spec-first Nephos HTTP API.
package apiserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

type server struct {
	build     version.Info
	ready     func() bool
	resources *service.Network
	instances *service.Instances
	events    *store.Store
	console   consoleRuntime
}

type consoleRuntime interface {
	Exec(context.Context, compute.RuntimeID, compute.ExecRequest) (compute.ExecSession, error)
}

// New returns the generated routes behind M1's localhost and bearer boundary.
func New(token string, build version.Info, ready func() bool, resources *service.Network, instances *service.Instances, events *store.Store, console consoleRuntime) http.Handler {
	h := generated.HandlerWithOptions(&server{build: build, ready: ready, resources: resources, instances: instances, events: events, console: console},
		generated.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", err.Error(), "")
		}})
	return localOnly(requireBearer(token, h))
}

func (s *server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	status := generated.Starting
	code := http.StatusServiceUnavailable
	if s.ready() {
		status = generated.Ready
		code = http.StatusOK
	}
	writeJSON(w, code, generated.HealthResponse{Status: status})
}

func (s *server) GetVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, generated.VersionResponse{
		ApiVersion:   "v1",
		BuildVersion: s.build.Version,
		BuildCommit:  s.build.Commit,
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
