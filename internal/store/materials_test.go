package store

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestMaterialsStoreLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewMaterialsStore(newIO(dir))
	lib, err := s.Load()
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if lib == nil {
		t.Fatal("Load should never return nil")
	}
	if len(lib.Items) != 0 {
		t.Fatalf("expected empty items, got %d", len(lib.Items))
	}
}

func TestMaterialsStoreAddAssignsIDAndTime(t *testing.T) {
	dir := t.TempDir()
	s := NewMaterialsStore(newIO(dir))

	it, err := s.Add(domain.MaterialItem{
		Category: domain.MaterialCategoryNaming,
		Title:    "测试命名表",
		Content:  "测试内容",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if it.ID != "mat-001" {
		t.Fatalf("first ID should be mat-001, got %q", it.ID)
	}
	if it.AddedAt.IsZero() {
		t.Fatal("AddedAt should be auto-filled")
	}

	// 第二条递增到 mat-002
	it2, _ := s.Add(domain.MaterialItem{Title: "x", Content: "y"})
	if it2.ID != "mat-002" {
		t.Fatalf("second ID should be mat-002, got %q", it2.ID)
	}

	// Load 看到 2 条
	lib, _ := s.Load()
	if len(lib.Items) != 2 {
		t.Fatalf("expected 2 items after 2 adds, got %d", len(lib.Items))
	}
}

func TestMaterialsStoreAddRequiresTitleAndContent(t *testing.T) {
	dir := t.TempDir()
	s := NewMaterialsStore(newIO(dir))

	if _, err := s.Add(domain.MaterialItem{Title: "x"}); err == nil {
		t.Fatal("Add without content should error")
	}
	if _, err := s.Add(domain.MaterialItem{Content: "x"}); err == nil {
		t.Fatal("Add without title should error")
	}
}

func TestMaterialsStoreAddBatchAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewMaterialsStore(newIO(dir))

	// 任一条无效 → 整批不写
	_, err := s.AddBatch([]domain.MaterialItem{
		{Title: "a", Content: "1"},
		{Title: "", Content: "2"}, // 触发错误
	})
	if err == nil {
		t.Fatal("batch with empty title should error")
	}
	lib, _ := s.Load()
	if len(lib.Items) != 0 {
		t.Fatalf("batch should be atomic, but got %d items", len(lib.Items))
	}

	// 全部合法 → 全部入库
	saved, err := s.AddBatch([]domain.MaterialItem{
		{Title: "a", Content: "1"},
		{Title: "b", Content: "2", Category: "custom"},
	})
	if err != nil {
		t.Fatalf("valid batch: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved, got %d", len(saved))
	}
	// ID 连续递增
	if saved[0].ID != "mat-001" || saved[1].ID != "mat-002" {
		t.Fatalf("IDs not sequential: %+v", saved)
	}
	// 空 category 默认归 reference
	if saved[0].Category != domain.MaterialCategoryReference {
		t.Fatalf("default category should be reference, got %q", saved[0].Category)
	}
	// 显式 category 透传
	if saved[1].Category != "custom" {
		t.Fatalf("explicit category should pass through, got %q", saved[1].Category)
	}
}

func TestMaterialsStoreRemove(t *testing.T) {
	dir := t.TempDir()
	s := NewMaterialsStore(newIO(dir))

	it, _ := s.Add(domain.MaterialItem{Title: "x", Content: "y"})
	if err := s.Remove(it.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	lib, _ := s.Load()
	if len(lib.Items) != 0 {
		t.Fatalf("expected 0 items after remove, got %d", len(lib.Items))
	}

	// 再删同样的 ID → 报错
	err := s.Remove(it.ID)
	if err == nil {
		t.Fatal("remove non-existent should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should mention not found, got %v", err)
	}
}
