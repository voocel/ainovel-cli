package bootstrap

import "testing"

func TestWritingContest_Normalize_DedupTrim(t *testing.T) {
	wc := WritingContest{Personas: []string{" 乌贼 ", "土豆", "乌贼", "", "  "}}
	got := wc.Normalize()
	want := []string{"乌贼", "土豆"}
	if len(got.Personas) != len(want) {
		t.Fatalf("personas = %v, want %v", got.Personas, want)
	}
	for i := range want {
		if got.Personas[i] != want[i] {
			t.Fatalf("personas[%d] = %q, want %q", i, got.Personas[i], want[i])
		}
	}
}

func TestWritingContest_Enabled(t *testing.T) {
	if (WritingContest{}).Enabled() {
		t.Fatal("空配置应为未启用")
	}
	if !(WritingContest{Personas: []string{"乌贼", "土豆"}}).Enabled() {
		t.Fatal("两个 persona 应为启用")
	}
	if (WritingContest{Personas: []string{"乌贼"}}).Normalize().Enabled() {
		t.Fatal("单 persona 不应启用竞稿")
	}
}

// TestMergeConfig_WritingContest 防止 mergeConfig 丢失 writing_contest：
// 项目级 ./ainovel.json 与 --config 覆盖都走 mergeConfig，若不合并该字段，
// 竞稿配置会在合并时被静默丢弃（仅全局 ~/.ainovel/config.json 直接赋值不受影响）。
func TestMergeConfig_WritingContest(t *testing.T) {
	// overlay 带竞稿配置 → 应合并进结果
	got := mergeConfig(Config{}, Config{
		WritingContest: WritingContest{Personas: []string{"乌贼", "土豆"}},
	})
	if len(got.WritingContest.Personas) != 2 {
		t.Fatalf("overlay 的 writing_contest 未合并: %+v", got.WritingContest)
	}

	// overlay 为空 → 保留 base 的竞稿配置
	base := Config{WritingContest: WritingContest{Personas: []string{"卖报小郎君", "土豆"}}}
	kept := mergeConfig(base, Config{})
	if len(kept.WritingContest.Personas) != 2 {
		t.Fatalf("overlay 为空时应保留 base 的 writing_contest: %+v", kept.WritingContest)
	}
}
