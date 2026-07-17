package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFoundationMissingReturnsReadError(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outline.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FoundationMissing(); err == nil {
		t.Fatal("损坏的大纲必须返回读取错误，不能降级成缺失项")
	}
}

func TestClearHandledSteerKeepsIntentWhenProgressReadFails(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.RunMeta.Init("default", "test", "model"); err != nil {
		t.Fatalf("RunMeta.Init: %v", err)
	}
	if err := st.RunMeta.SetPendingSteer("保留这条干预"); err != nil {
		t.Fatalf("SetPendingSteer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearHandledSteer(); err == nil {
		t.Fatal("corrupt progress should make ClearHandledSteer fail")
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatalf("RunMeta.Load: %v", err)
	}
	if meta == nil || meta.PendingSteer != "保留这条干预" {
		t.Fatalf("recovery intent was lost after partial clear: %+v", meta)
	}
}
