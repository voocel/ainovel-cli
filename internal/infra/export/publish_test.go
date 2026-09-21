package export

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func content(text string) func(io.Writer) error {
	return func(w io.Writer) error { _, err := io.WriteString(w, text); return err }
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q (%v), want %q", path, got, err, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist: %v", path, err)
	}
}

func TestPublishIsAtomicAndExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "作品.txt")
	ctx := context.Background()
	if err := Publish(ctx, path, false, content("已确认正文")); err != nil {
		t.Fatal(err)
	}
	if err := Publish(ctx, path, false, content("其他")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing output: %v", err)
	}
	assertFile(t, path, "已确认正文")

	failed := func(w io.Writer) error { io.WriteString(w, "半成品"); return errors.New("write failed") }
	if err := Publish(ctx, path, true, failed); err == nil {
		t.Fatal("write failure ignored")
	}
	assertFile(t, path, "已确认正文")
	fresh := filepath.Join(dir, "新建.txt")
	if err := Publish(ctx, fresh, false, failed); err == nil {
		t.Fatal("write failure ignored")
	}
	assertMissing(t, fresh)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := Publish(cancelled, fresh, true, content("取消")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	assertMissing(t, fresh)

	if err := Publish(ctx, path, true, content("新正文")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "新正文")
	if files, _ := os.ReadDir(dir); len(files) != 1 {
		t.Fatalf("temporary files leaked: %v", files)
	}
}
