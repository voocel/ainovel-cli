package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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
	if _, err := second.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatalf("mark foreign version: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	third, err := Open(ctx, path)
	if err == nil {
		third.Close()
		t.Fatal("database with a foreign schema version must be rejected, not migrated")
	}
	// 报错要让用户知道是哪个库、怎样重新开始。
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "改名备份") {
		t.Fatalf("unsupported schema error lacks the path or the way out: %v", err)
	}
}
