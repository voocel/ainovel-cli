package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEnsureSchemaIsIdempotentAndRejectsForeignVersions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ainovel.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	var version int
	if err := first.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("fresh schema version = %d, %v; want %d", version, err, schemaVersion)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close fresh database: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen at current version must succeed: %v", err)
	}
	if _, err := second.db.ExecContext(ctx, "PRAGMA user_version = 7"); err != nil {
		t.Fatalf("mark foreign version: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	if third, err := Open(ctx, path); err == nil {
		third.Close()
		t.Fatal("database with a foreign schema version must be rejected, not migrated")
	}
}
