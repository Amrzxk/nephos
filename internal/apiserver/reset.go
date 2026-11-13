package apiserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
)

const (
	defaultResetTimeout = 300
	maxResetTimeout     = 1800
)

// WithReset enables POST /v1/workspaces/default/reset.
func WithReset(reset *service.Reset) Option {
	return func(s *server) { s.reset = reset }
}

func (s *server) ResetWorkspace(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.ResetWorkspaceParams) {
	if !checkWorkspace(w, workspace) {
		return
	}
	if s.reset == nil || !s.ready() {
		writeAPIError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "the appliance is not ready to reset", "")
		return
	}
	seconds := defaultResetTimeout
	if params.Timeout != nil {
		seconds = *params.Timeout
	}
	if seconds < 1 || seconds > maxResetTimeout {
		writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "timeout must be between 1 and 1800 seconds", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(seconds)*time.Second)
	defer cancel()
	result, err := s.reset.Run(ctx)
	var leak *service.LeakError
	if errors.As(err, &leak) {
		check := leakCheck(leak.Result)
		writeJSON(w, http.StatusConflict, generated.ResetError{Code: "LeakDetected", Message: leak.Error(), LeakCheck: &check})
		return
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generated.ResetResult{Workspace: result.Workspace, LeakCheck: leakCheck(result.LeakCheck)})
}

func leakCheck(result model.LeakResult) generated.LeakCheck {
	check := generated.LeakCheck{Complete: result.Complete, Clean: result.Clean,
		Findings: make([]generated.LeakFinding, 0, len(result.Findings)), InspectionErrors: result.InspectionErrors}
	if check.InspectionErrors == nil {
		check.InspectionErrors = []string{}
	}
	for _, f := range result.Findings {
		check.Findings = append(check.Findings, generated.LeakFinding{Kind: f.Kind, Identity: f.Identity, Reason: f.Reason})
	}
	return check
}
