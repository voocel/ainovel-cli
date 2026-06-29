package store

import (
	"fmt"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestDirectivesLoadEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	list, err := store.Directives.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestDirectivesAddAndLoad(t *testing.T) {
	store := NewStore(t.TempDir())

	list, err := store.Directives.Add(domain.UserDirective{Text: "对话占比提高", Chapter: 3, CreatedAt: "2026-06-11T10:00:00+08:00"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(list) != 1 || list[0].Text != "对话占比提高" {
		t.Fatalf("unexpected list after add: %+v", list)
	}

	loaded, err := store.Directives.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Chapter != 3 {
		t.Errorf("unexpected loaded list: %+v", loaded)
	}
}

func TestDirectivesAddDeduplicates(t *testing.T) {
	store := NewStore(t.TempDir())

	if _, err := store.Directives.Add(domain.UserDirective{Text: "Tiêu đề只用中文"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := store.Directives.Add(domain.UserDirective{Text: "Tiêu đề只用中文"})
	if err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("duplicate text should not be appended, got %d entries", len(list))
	}
}

func TestDirectivesRemove(t *testing.T) {
	store := NewStore(t.TempDir())

	for i := 1; i <= 3; i++ {
		if _, err := store.Directives.Add(domain.UserDirective{Text: fmt.Sprintf("要求%d", i)}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	list, err := store.Directives.Remove(2)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(list) != 2 || list[0].Text != "要求1" || list[1].Text != "要求3" {
		t.Errorf("unexpected list after remove: %+v", list)
	}

	if _, err := store.Directives.Remove(5); err == nil {
		t.Error("expected error for out-of-range index")
	}
	if _, err := store.Directives.Remove(0); err == nil {
		t.Error("expected error for index 0")
	}
}

func TestDirectivesAddCapacityLimit(t *testing.T) {
	store := NewStore(t.TempDir())

	for i := 1; i <= maxDirectives; i++ {
		if _, err := store.Directives.Add(domain.UserDirective{Text: fmt.Sprintf("要求%d", i)}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if _, err := store.Directives.Add(domain.UserDirective{Text: "超限的要求"}); err == nil {
		t.Error("expected error when exceeding capacity")
	}

	// 删一条后应能再加
	if _, err := store.Directives.Remove(1); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Directives.Add(domain.UserDirective{Text: "腾出名额后的要求"}); err != nil {
		t.Errorf("Add after remove should succeed: %v", err)
	}
}
