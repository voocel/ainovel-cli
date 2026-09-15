package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DiagnosticOpenError separates a shareable failure category from its local detail.
type DiagnosticOpenError struct {
	Code string
	Err  error
}

func (e *DiagnosticOpenError) Error() string {
	return fmt.Sprintf("open diagnostic database: %v", e.Err)
}
func (e *DiagnosticOpenError) Unwrap() error { return e.Err }

func diagnosticOpenError(err error) error {
	code := "open_failed"
	var databaseError *sqlite.Error
	switch {
	case errors.Is(err, os.ErrNotExist):
		code = "missing"
	case errors.Is(err, os.ErrPermission):
		code = "permission"
	case errors.As(err, &databaseError):
		// Extended result codes retain the primary SQLite code in the low byte.
		switch databaseError.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			code = "corrupt"
		case sqlite3.SQLITE_PERM, sqlite3.SQLITE_AUTH:
			code = "permission"
		}
	}
	return &DiagnosticOpenError{Code: code, Err: err}
}

// OpenReadOnly neither creates a missing database nor initializes its schema.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, diagnosticOpenError(err)
	}
	// SQLite CANTOPEN does not distinguish missing files from permissions. A
	// read-only OS open preserves that evidence without creating anything.
	file, err := os.Open(abs)
	if err != nil {
		return nil, diagnosticOpenError(err)
	}
	if err := file.Close(); err != nil {
		return nil, diagnosticOpenError(err)
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := &url.URL{Scheme: "file", Path: uriPath}
	q := uri.Query()
	q.Set("mode", "ro")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, diagnosticOpenError(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, diagnosticOpenError(err)
	}
	if version != schemaVersion {
		db.Close()
		return nil, &DiagnosticOpenError{Code: "unsupported_schema", Err: fmt.Errorf("database schema version %d is not supported (expected %d)", version, schemaVersion)}
	}
	return &Store{db: db}, nil
}
