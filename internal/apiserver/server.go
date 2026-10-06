// Package apiserver serves the spec-first Nephos HTTP API.
package apiserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

type server struct {
	build        version.Info
	ready        func() bool
	resources    *service.Network
	instances    *service.Instances
	events       *store.Store
	console      consoleRuntime
	admitConsole func(context.Context, model.Instance) (context.Context, func() error, error)
	references   *lifecycle.Registry
}

type consoleRuntime interface {
	Exec(context.Context, compute.Reference, compute.ExecRequest) (compute.ExecSession, error)
}

// New returns the generated routes behind M1's localhost and bearer boundary.
func New(token string, build version.Info, ready func() bool, resources *service.Network, instances *service.Instances, events *store.Store, console consoleRuntime, options ...Option) http.Handler {
	s := &server{build: build, ready: ready, resources: resources, instances: instances, events: events, console: console}
	for _, option := range options {
		option(s)
	}
	h := generated.HandlerWithOptions(s,
		generated.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", err.Error(), "")
		}})
	return localOnly(requireBearer(token, h))
}

// Option supplies internal daemon wiring, not learner-visible API behavior.
type Option func(*server)

// WithConsoleLifecycle owns admission until exec cleanup and stream workers
// finish. The daemon supplies admission; reset adds its durable fence there.
func WithConsoleLifecycle(admit func(context.Context, model.Instance) (context.Context, func() error, error), references *lifecycle.Registry) Option {
	return func(s *server) { s.admitConsole = admit; s.references = references }
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
