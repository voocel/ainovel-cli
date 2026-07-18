package logger

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupFileWritesDefaultLog(t *testing.T) {
	previous := slog.Default()

	dir := t.TempDir()
	cleanup, err := SetupFile(dir, "test.log", false)
	if err != nil {
		t.Fatalf("SetupFile: %v", err)
	}
	slog.Info("logger-test-message")
	cleanup()
	if slog.Default() != previous {
		t.Fatal("cleanup 应恢复先前的默认 logger")
	}

	data, err := os.ReadFile(filepath.Join(dir, "logs", "test.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "logger-test-message") {
		t.Fatalf("log missing message: %q", data)
	}
}

func TestSetupFileReturnsOpenError(t *testing.T) {
	previous := slog.Default()
	var fallback bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&fallback, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := SetupFile(blocker, "test.log", false)
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("日志目录不可创建时应返回错误")
	}
	if cleanup != nil {
		t.Fatal("失败时不应返回清理函数")
	}
	slog.Info("fallback-remains-visible")
	if !strings.Contains(fallback.String(), "fallback-remains-visible") {
		t.Fatal("文件日志初始化失败后应保留原默认 logger")
	}
}
