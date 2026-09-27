package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigratesAndReopensWithoutLosingState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "nephos.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state DB mode=%#o, want 0600", got)
	}

	var workspaceCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces").Scan(&workspaceCount); err != nil {
		t.Fatal(err)
	}
	if workspaceCount != 1 {
		t.Fatalf("workspace count=%d, want 1", workspaceCount)
	}
	var workspaceID string
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM workspaces").Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if workspaceID != "default" {
		t.Fatalf("workspace=%q, want default", workspaceID)
	}

	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q, want wal", mode)
	}
	var enabled, version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys=%d, want 1", enabled)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version=%d, want 3", version)
	}
	var migrationCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration count=%d, want 1", migrationCount)
	}

	_, err = s.db.ExecContext(ctx, "INSERT INTO events(workspace_id, resource_type, resource_id, action, generation, created_at) VALUES ('default', 'vpc', 'vpc-0123456789abcdef0', 'created', 1, 100)")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("event count after reopen=%d, want 1", migrationCount)
	}
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 3 {
		t.Fatalf("migration rows after reopen=%d, want 3", migrationCount)
	}
}

func TestOpenUpgradesVersionOneWithoutLosingEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, initialMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(workspace_id, resource_type, resource_id, action, generation, created_at) VALUES ('default', 'vpc', 'vpc-0123456789abcdef0', 'created', 1, 100)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version, eventCount, migrationCount int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if version != 3 || eventCount != 1 || migrationCount != 3 {
		t.Fatalf("upgraded version=%d events=%d migrations=%d", version, eventCount, migrationCount)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO idempotency_requests(workspace_id, operation, key, payload_hash, resource_id, response_json, created_at, expires_at) VALUES ('default', 'create-vpc', 'old-key', 'hash', 'vpc-0123456789abcdef0', '{}', 100, 200)")
	if err != nil {
		t.Fatalf("new idempotency snapshot column unavailable: %v", err)
	}
}

func TestOpenUpgradesVersionTwoAndPreservesDeleteIntent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old-v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, initialMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, idempotencyResultMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES ('vpc', 'vpc-0123456789abcdef0')"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO vpcs(id, workspace_id, short_index, name, cidr_block, generation, state) VALUES ('vpc-0123456789abcdef0', 'default', 1, 'old', '10.0.0.0/16', 2, 'deleting')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	vpc, err := s.GetVPC(ctx, "default", "vpc-0123456789abcdef0")
	if err != nil {
		t.Fatal(err)
	}
	if !vpc.DeletionRequested || vpc.State != "deleting" {
		t.Fatalf("upgraded VPC=%+v", vpc)
	}
}

func TestOpenEnforcesSubnetForeignKey(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "nephos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	result, err := s.db.ExecContext(ctx, "INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES ('subnet', 'subnet-0123456789abcdef0')")
	if err != nil {
		t.Fatal(err)
	}
	shortIndex, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO subnets(id, workspace_id, vpc_id, name, cidr_block, availability_zone, short_index) VALUES (?, 'default', 'vpc-fffffffffffffffff', 'isolated', '10.0.1.0/24', 'local-1a', ?)", "subnet-0123456789abcdef0", shortIndex)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("invalid VPC foreign key error=%v", err)
	}
}

func TestOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(context.Background(), path); err == nil {
		s.Close()
		t.Fatal("future schema version was accepted")
	}
}

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err == nil {
		s.Close()
		t.Fatal("canceled open succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open error=%v", err)
	}
}
