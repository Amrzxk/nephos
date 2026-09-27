package apiserver

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/store"
)

type eventBody struct {
	ID           int64  `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Action       string `json:"action"`
	State        string `json:"state"`
	Generation   int64  `json:"generation"`
	Message      string `json:"message"`
	CreatedAt    int64  `json:"created_at"`
}

func eventResponse(event store.Event) eventBody {
	return eventBody{ID: event.ID, WorkspaceID: event.WorkspaceID,
		ResourceType: event.ResourceType, ResourceID: event.ResourceID,
		Action: event.Action, State: event.State, Generation: event.Generation,
		Message: event.Message, CreatedAt: event.CreatedAt}
}

// GetEvents streams SQLite-backed events and resumes strictly after the last
// event ID. A client disconnect never removes the durable event log.
func (s *server) GetEvents(w http.ResponseWriter, r *http.Request, params generated.GetEventsParams) {
	if s.events == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "event store is not ready", "")
		return
	}
	cursor := int64(0)
	if params.LastEventID != nil {
		parsed, err := strconv.ParseInt(*params.LastEventID, 10, 64)
		if err != nil || parsed < 0 {
			writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "Last-Event-ID must be a nonnegative integer", "")
			return
		}
		cursor = parsed
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "InternalError", "event stream cannot flush", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := s.events.EventsAfter(r.Context(), cursor, 100)
		if err != nil {
			if r.Context().Err() == nil {
				slog.Error("event stream lost its durable cursor", "after_event_id", cursor, "error", err)
			}
			// Headers are already sent; terminate so the client can reconnect.
			return
		}
		for _, event := range events {
			payload, err := json.Marshal(eventResponse(event))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.ID, payload); err != nil {
				return
			}
			cursor = event.ID
		}
		if len(events) != 0 {
			flusher.Flush()
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
