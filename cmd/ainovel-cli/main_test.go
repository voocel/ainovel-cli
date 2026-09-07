package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Fatalf("version = %q, want %q", got, version)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHelpPrintsUsageAndExitsZero(t *testing.T) {
	// --help 是程序级入口（workbench §3）：输出帮助并正常退出，不是错误。
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --help: %v", err)
	}
	if !strings.Contains(stdout.String(), "用法") || !strings.Contains(stdout.String(), "--headless") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestRunRequiresExplicitHeadlessForCommands(t *testing.T) {
	// 入口契约（workbench §3）：无参数只进 TUI，非交互动作必须显式 --headless。
	var stdout, stderr bytes.Buffer
	err := run([]string{"quick", "write"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--headless") {
		t.Fatalf("error = %v, want headless guidance", err)
	}
}

func TestRunHeadlessRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"--headless", "unknown"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "未知命令") {
		t.Fatalf("error = %v, want unknown command", err)
	}
}

func TestRunHeadlessWithoutActionPrintsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--headless"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "quick write") {
		t.Fatalf("help output = %q", stdout.String())
	}
}
