// Package store owns Nephos's SQLite desired state and durable event log.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
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

//go:embed migrations/0004_instance_deletion.sql
var instanceDeletionMigration string

//go:embed migrations/0005_instance_provisioned.sql
var instanceProvisionedMigration string

const schemaVersion = 5

// Store is the sole durable authority for resource state. One connection at a
// time serializes SQLite writes; the DSN reapplies connection-local PRAGMAs
// whenever database/sql replaces an interrupted connection.
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

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve state file path: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: absPath}
	params := uri.Query()
	params.Set("_busy_timeout", "5000")
	params.Set("_foreign_keys", "on")
	uri.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", uri.String())
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

	var busyTimeout, foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return nil, fmt.Errorf("verify SQLite busy timeout: %w", err)
	}
	if busyTimeout != 5000 {
		return nil, fmt.Errorf("SQLite refused busy timeout: %d", busyTimeout)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return nil, fmt.Errorf("verify SQLite foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return nil, fmt.Errorf("SQLite refused foreign keys: %d", foreignKeys)
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
	case 3:
		if _, err := tx.ExecContext(ctx, instanceDeletionMigration); err != nil {
			return nil, fmt.Errorf("apply state migration 4: %w", err)
		}
		fallthrough
	case 4:
		if _, err := tx.ExecContext(ctx, instanceProvisionedMigration); err != nil {
			return nil, fmt.Errorf("apply state migration 5: %w", err)
		}
		fallthrough
	case schemaVersion:
		// All migration records must agree with the advertised schema.
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version IN (1, 2, 3, 4, 5)").Scan(&count); err != nil {
			return nil, fmt.Errorf("verify state migrations: %w", err)
		}
		if count != schemaVersion {
			return nil, fmt.Errorf("state migration metadata incomplete: %d of %d", count, schemaVersion)
		}
	default:
		return nil, fmt.Errorf("unsupported state schema version %d (supported: %d)", version, schemaVersion)
	}
	if version != schemaVersion {
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
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
