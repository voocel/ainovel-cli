package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTripAndMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if state, err := LoadState(dir); err != nil || state.LastProjectID != "" {
		t.Fatalf("missing state = %#v, %v, want empty ok", state, err)
	}
	if err := SaveState(dir, State{LastProjectID: "book-1"}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if state, err := LoadState(dir); err != nil || state.LastProjectID != "book-1" {
		t.Fatalf("loaded state = %#v, %v", state, err)
	}
}

// TestStateCorruptedFileReturnsError 守卫"读不出来不得回写"（Codex 复审）：
// 损坏的偏好文件必须显式报错，静默按空处理会让下一次读改写抹掉全部偏好。
func TestStateCorruptedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StatePath(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write corrupted state: %v", err)
	}
	state, err := LoadState(dir)
	if err == nil {
		t.Fatal("corrupted state must return error")
	}
	if state.LastProjectID != "" || state.Collapsed != nil {
		t.Fatalf("corrupted load must come with empty state: %#v", state)
	}
}
