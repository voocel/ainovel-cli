package bootstrap

import "testing"

// TestWritingContest_SynopsisMode 验证 mode 解析与 Normalize 保留。
func TestWritingContest_SynopsisMode(t *testing.T) {
	if (WritingContest{}).SynopsisMode() {
		t.Fatal("空 mode 不应为 synopsis")
	}
	if !(WritingContest{Mode: "synopsis"}).SynopsisMode() {
		t.Fatal("mode=synopsis 应生效")
	}
	if !(WritingContest{Mode: " Synopsis "}).SynopsisMode() {
		t.Fatal("应忽略大小写与空白")
	}
	got := WritingContest{Personas: []string{"乌贼", "土豆"}, Mode: "synopsis"}.Normalize()
	if !got.SynopsisMode() {
		t.Fatal("Normalize 丢失了 Mode")
	}
}

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

// TestWritingContest_Normalize_KeepsConcurrency 验证归一化保留 concurrency 开关。
func TestWritingContest_Normalize_KeepsConcurrency(t *testing.T) {
	wc := WritingContest{Personas: []string{"乌贼", "土豆"}, Concurrency: true}
	got := wc.Normalize()
	if !got.Concurrency {
		t.Fatal("Normalize 丢失了 Concurrency=true")
	}
	off := WritingContest{Personas: []string{"乌贼", "土豆"}}.Normalize()
	if off.Concurrency {
		t.Fatal("未设置时 Concurrency 应为 false")
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
