package store

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

// Event is an immutable, monotonically numbered durable state change.
type Event struct {
	ID           int64
	WorkspaceID  string
	ResourceType string
	ResourceID   string
	Action       string
	State        string
	Generation   int64
	Message      string
	CreatedAt    int64
}

// AppendEvent adds a resource state transition to the current transaction.
func (tx *Tx) AppendEvent(ctx context.Context, event Event) error {
	if err := tx.q.InsertEvent(ctx, sqlc.InsertEventParams{
		WorkspaceID: event.WorkspaceID, ResourceType: event.ResourceType,
		ResourceID: event.ResourceID, Action: event.Action, State: event.State,
		Generation: event.Generation, Message: event.Message, CreatedAt: event.CreatedAt,
	}); err != nil {
		return fmt.Errorf("append %s event for %s: %w", event.Action, event.ResourceID, err)
	}
	return nil
}

// EventsAfter returns events with IDs strictly greater than id in ascending
// order. The database, rather than an in-memory queue, is the SSE resume log.
func (s *Store) EventsAfter(ctx context.Context, id int64, limit int) ([]Event, error) {
	if id < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid event cursor or limit")
	}
	rows, err := sqlc.New(s.db).EventsAfter(ctx, sqlc.EventsAfterParams{ID: id, Limit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("read events after %d: %w", id, err)
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, Event{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType,
			ResourceID: row.ResourceID, Action: row.Action, State: row.State,
			Generation: row.Generation, Message: row.Message, CreatedAt: row.CreatedAt,
		})
	}
	return events, nil
}
