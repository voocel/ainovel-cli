package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSaveAndLoadHandoffPack(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pack := domain.HandoffPack{
		Reason:          "review-ch08-accept",
		NovelName:       "测试小说",
		NextChapter:     9,
		CompletedCount:  8,
		RecentSummaries: []string{"第7章：试炼升级", "第8章：主角败而后立"},
	}
	if err := s.SaveHandoffPack(pack); err != nil {
		t.Fatalf("SaveHandoffPack: %v", err)
	}

	got, err := s.LoadHandoffPack()
	if err != nil {
		t.Fatalf("LoadHandoffPack: %v", err)
	}
	if got == nil {
		t.Fatal("expected handoff pack, got nil")
	}
	if got.Reason != pack.Reason || got.NextChapter != 9 {
		t.Fatalf("unexpected handoff pack: %+v", got)
	}
}
