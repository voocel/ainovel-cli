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

// TestNormalizeDeadEntities 验证：别名 key 折算到正名；同正名多条取最早死亡章。
func TestNormalizeDeadEntities(t *testing.T) {
	alias := map[string]string{"五爷": "王老五", "老五": "王老五"}
	dead := map[string]int{
		"五爷":  7, // 别名 → 折到 王老五
		"老五":  3, // 同正名另一别名，章更早 → 取 3
		"王老五": 5, // 正名直记
		"李四":  2, // 无别名映射 → 原样保留
	}
	got := NormalizeDeadEntities(dead, alias)
	if len(got) != 2 {
		t.Fatalf("got = %v, want 2 个正名", got)
	}
	if got["王老五"] != 3 {
		t.Fatalf("got[王老五] = %d, want 最早章 3", got["王老五"])
	}
	if got["李四"] != 2 {
		t.Fatalf("got[李四] = %d, want 2", got["李四"])
	}
	// alias 表为 nil 时原样返回
	if got := NormalizeDeadEntities(dead, nil); len(got) != len(dead) {
		t.Fatalf("nil alias: got = %v, want 原样", got)
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
