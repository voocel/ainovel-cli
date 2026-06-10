package domain

import "testing"

// TestOverdueForeshadow 验证逾期判定：deadline>0 且 current>=deadline 且未回收。
func TestOverdueForeshadow(t *testing.T) {
	entries := []ForeshadowEntry{
		{ID: "f1", Status: "planted", Deadline: 10},  // 逾期（current=10）
		{ID: "f2", Status: "advanced", Deadline: 11}, // 未到期
		{ID: "f3", Status: "resolved", Deadline: 5},  // 已回收，豁免
		{ID: "f4", Status: "planted"},                // 无 deadline，豁免
		{ID: "f5", Status: "planted", Deadline: 3},   // 逾期
	}
	got := OverdueForeshadow(entries, 10)
	if len(got) != 2 {
		t.Fatalf("overdue = %d 条, want 2: %+v", len(got), got)
	}
	if got[0].ID != "f1" || got[1].ID != "f5" {
		t.Fatalf("overdue ids = %s,%s, want f1,f5", got[0].ID, got[1].ID)
	}
}
