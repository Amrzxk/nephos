// Package store owns Nephos's SQLite desired state and durable event log.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed migrations/0001_initial.sql
var initialMigration string

//go:embed migrations/0002_idempotency_result.sql
var idempotencyResultMigration string

//go:embed migrations/0003_deletion_intent.sql
var deletionIntentMigration string

const schemaVersion = 3

// Store is the sole durable authority for resource state. Its single database
// connection keeps SQLite connection-local PRAGMAs consistent.
type Store struct {
	db *sql.DB
}

// Open prepares a private SQLite file, enables WAL and foreign keys, and
// applies any embedded migrations before returning.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	if path == "" {
		return nil, fmt.Errorf("open state: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create state file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close state file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("protect state file: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	ready := false
	defer func() {
		if !ready {
			_ = db.Close()
		}
	}()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return nil, fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if mode != "wal" {
		return nil, fmt.Errorf("SQLite refused WAL mode: %s", mode)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin state migration: %w", err)
	}
	// Rollback after Commit returns ErrTxDone; on failure the outer defer closes db.
	defer func() { _ = tx.Rollback() }()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, fmt.Errorf("read state schema version: %w", err)
	}
	switch version {
	case 0:
		if _, err := tx.ExecContext(ctx, initialMigration); err != nil {
			return nil, fmt.Errorf("apply state migration 1: %w", err)
		}
		fallthrough
	case 1:
		if _, err := tx.ExecContext(ctx, idempotencyResultMigration); err != nil {
			return nil, fmt.Errorf("apply state migration 2: %w", err)
		}
		fallthrough
	case 2:
		if _, err := tx.ExecContext(ctx, deletionIntentMigration); err != nil {
			return nil, fmt.Errorf("apply state migration 3: %w", err)
		}
		fallthrough
	case schemaVersion:
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version IN (1, 2, 3)").Scan(&count); err != nil {
			return nil, fmt.Errorf("verify state migrations: %w", err)
		}
		if count != schemaVersion {
			return nil, fmt.Errorf("state migration metadata incomplete: %d of %d", count, schemaVersion)
		}
	default:
		return nil, fmt.Errorf("unsupported state schema version %d (supported: %d)", version, schemaVersion)
	}
	if version != schemaVersion {
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
			return nil, fmt.Errorf("record state schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit state migration: %w", err)
	}
	ready = true
	return &Store{db: db}, nil
}

// Close releases the store's database connection.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	return nil
}
