package domain

import "testing"

func TestIsDeadValue(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"死亡", true}, {"战死沙场", true}, {"已死", true}, {"身亡", true},
		{"陨落", true}, {"魂飞魄散", true},
		{"假死脱身", false}, {"濒死", false}, {"重伤垂死", false},
		{"健在", false}, {"重伤", false}, {"诈死", false},
	}
	for _, c := range cases {
		if got := IsDeadValue(c.v); got != c.want {
			t.Errorf("IsDeadValue(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// TestDeadEntities 验证：取最新 status、死亡章早于 current 才算、复活豁免、同章死亡豁免。
func TestDeadEntities(t *testing.T) {
	changes := []StateChange{
		{Chapter: 3, Entity: "甲", Field: "status", NewValue: "死亡"},
		{Chapter: 2, Entity: "乙", Field: "status", NewValue: "死亡"},
		{Chapter: 4, Entity: "乙", Field: "status", NewValue: "复活归来"}, // 最新非死亡 → 豁免
		{Chapter: 5, Entity: "丙", Field: "status", NewValue: "战死"},    // 同章死亡 → 豁免（current=5）
		{Chapter: 1, Entity: "丁", Field: "realm", NewValue: "金丹"},     // 非 status → 忽略
	}
	dead := DeadEntities(changes, 5)
	if len(dead) != 1 {
		t.Fatalf("dead = %v, want 仅 甲", dead)
	}
	if ch, ok := dead["甲"]; !ok || ch != 3 {
		t.Fatalf("dead[甲] = %d,%v, want 3,true", ch, ok)
	}
}

// TestDeadEntitiesNormalized_AliasDeathCanonRevive 验证：
// 别名死亡（五爷 ch2 死亡）+ 正名复活（王老五 ch4 复活）→ fold 前归一后豁免，返回空。
func TestDeadEntitiesNormalized_AliasDeathCanonRevive(t *testing.T) {
	changes := []StateChange{
		{Chapter: 2, Entity: "五爷", Field: "status", NewValue: "战死"},    // 别名死亡
		{Chapter: 4, Entity: "王老五", Field: "status", NewValue: "复活归来"}, // 正名复活（最新非死亡）
	}
	alias := map[string]string{"五爷": "王老五"}
	dead := DeadEntitiesNormalized(changes, alias, 5)
	if len(dead) != 0 {
		t.Fatalf("别名死亡+正名复活应完全豁免，got %v", dead)
	}
}

func TestFoldStateChanges(t *testing.T) {
	changes := []StateChange{
		{Chapter: 1, Entity: "甲", Field: "realm", NewValue: "筑基"},
		{Chapter: 3, Entity: "甲", Field: "realm", NewValue: "金丹"},
		{Chapter: 2, Entity: "甲", Field: "location", NewValue: "青云山"},
	}
	folded := FoldStateChanges(changes)
	if folded["甲"]["realm"].NewValue != "金丹" || folded["甲"]["location"].NewValue != "青云山" {
		t.Fatalf("folded = %+v", folded["甲"])
	}
}
