// Package store owns Nephos's SQLite desired state and durable event log.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrations are the embedded schema steps, ordered by their NNNN_ prefix.
// Each file inserts its own schema_migrations row; versions must be 1..N.
var migrations = mustLoadMigrations()

// schemaVersion is the newest schema this binary understands.
var schemaVersion = len(migrations)

func mustLoadMigrations() []string {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		panic(fmt.Sprintf("read embedded migrations: %v", err)) // impossible: embedded at build time
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]string, 0, len(entries))
	for i, entry := range entries {
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%04d_", &version); err != nil || version != i+1 {
			panic(fmt.Sprintf("embedded migration %q is out of sequence (want version %d)", entry.Name(), i+1))
		}
		raw, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			panic(fmt.Sprintf("read embedded migration %q: %v", entry.Name(), err))
		}
		out = append(out, string(raw))
	}
	return out
}

// migrationSQL returns the SQL of one schema version (1-based).
func migrationSQL(version int) string { return migrations[version-1] }

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
	// SQLite URIs use forward slashes and need a leading slash before a
	// Windows drive letter (file:///C:/...). The appliance is Linux-only, but
	// the CLI tests run an in-process daemon on every supported host OS.
	uriPath := filepath.ToSlash(absPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
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
	if version < 0 || version > schemaVersion {
		return nil, fmt.Errorf("unsupported state schema version %d (supported: %d)", version, schemaVersion)
	}
	for next := version + 1; next <= schemaVersion; next++ {
		if _, err := tx.ExecContext(ctx, migrationSQL(next)); err != nil {
			return nil, fmt.Errorf("apply state migration %d: %w", next, err)
		}
	}
	// All migration records must agree with the advertised schema.
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 1 AND ?", schemaVersion).Scan(&count); err != nil {
		return nil, fmt.Errorf("verify state migrations: %w", err)
	}
	if count != schemaVersion {
		return nil, fmt.Errorf("state migration metadata incomplete: %d of %d", count, schemaVersion)
	}
	if version != schemaVersion {
		// PRAGMA takes no bind parameters; schemaVersion is a compile-time count.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
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
