package rules

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// findViolation 在Kết quả中按 rule + target 查找第一条违规。
func findViolation(vs []Violation, rule, target string) *Violation {
	for i := range vs {
		if vs[i].Rule == rule && vs[i].Target == target {
			return &vs[i]
		}
	}
	return nil
}

func TestCheck_EmptyStructured(t *testing.T) {
	vs := Check("任何内容", -1, Structured{})
	if vs != nil {
		t.Errorf("empty structured should return nil, got %+v", vs)
	}
}

func TestCheck_ForbiddenChars(t *testing.T) {
	text := "他笑了——又叹了口气——离去。"
	vs := Check(text, -1, Structured{
		ForbiddenChars: []string{"——"},
	})
	v := findViolation(vs, "forbidden_chars", "——")
	if v == nil {
		t.Fatal("expected forbidden_chars violation")
	}
	if v.Severity != SeverityError {
		t.Errorf("severity=%s, want error", v.Severity)
	}
	if v.Actual != 2 {
		t.Errorf("actual=%v, want 2", v.Actual)
	}
}

func TestCheck_ForbiddenCharsNotPresent(t *testing.T) {
	vs := Check("普通文本Không có违规", -1, Structured{
		ForbiddenChars: []string{"——"},
	})
	if len(vs) != 0 {
		t.Errorf("expected no violations, got %+v", vs)
	}
}

func TestCheck_ForbiddenPhrases(t *testing.T) {
	text := "不是……而是真相被掩盖了。这里探讨核心动机。"
	vs := Check(text, -1, Structured{
		ForbiddenPhrases: []string{"不是……而是", "核心动机"},
	})
	if len(vs) != 2 {
		t.Errorf("expected 2 violations, got %d: %+v", len(vs), vs)
	}
	for _, v := range vs {
		if v.Severity != SeverityError {
			t.Errorf("severity=%s, want error", v.Severity)
		}
	}
}

func TestCheck_FatigueWordsUnderLimit(t *testing.T) {
	text := "他不禁笑了。"
	vs := Check(text, -1, Structured{
		FatigueWords: map[string]int{"不禁": 1},
	})
	if len(vs) != 0 {
		t.Errorf("under limit should not violate, got %+v", vs)
	}
}

func TestCheck_FatigueWordsAtLimit(t *testing.T) {
	// limit=1，actual=1 → 不违规
	text := "他不禁笑了。"
	vs := Check(text, -1, Structured{
		FatigueWords: map[string]int{"不禁": 1},
	})
	if len(vs) != 0 {
		t.Errorf("at limit should not violate (limit 1 actual 1), got %+v", vs)
	}
}

func TestCheck_FatigueWordsOverLimit(t *testing.T) {
	// limit=1，actual=3 → warning
	text := "他不禁笑了，又不禁皱眉，最后不禁离去。"
	vs := Check(text, -1, Structured{
		FatigueWords: map[string]int{"不禁": 1},
	})
	v := findViolation(vs, "fatigue_words", "不禁")
	if v == nil {
		t.Fatal("expected fatigue_words violation")
	}
	if v.Severity != SeverityWarning {
		t.Errorf("severity=%s, want warning", v.Severity)
	}
	if v.Limit != 1 {
		t.Errorf("limit=%v, want 1", v.Limit)
	}
	if v.Actual != 3 {
		t.Errorf("actual=%v, want 3", v.Actual)
	}
}

// 字数边界测试
// 范围 3000-6000:
//   actual 3000 → 在范围 → no violation
//   actual 2999 → deviation ≈ 0.033% → warning
//   actual 2401 → deviation = 599/3000 ≈ 19.97% → warning
//   actual 2400 → deviation = 600/3000 = 20% → error（>= threshold）
//   actual 6001 → deviation ≈ 0.017% → warning
//   actual 7199 → deviation ≈ 19.98% → warning
//   actual 7200 → deviation = 1200/6000 = 20% → error

func TestCheck_ChapterWordsInRange(t *testing.T) {
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check("", 4000, Structured{ChapterWords: rng})
	if len(vs) != 0 {
		t.Errorf("in range should yield no violation, got %+v", vs)
	}
	// 边界值
	vs = Check("", 3000, Structured{ChapterWords: rng})
	if len(vs) != 0 {
		t.Errorf("at min should be in range, got %+v", vs)
	}
	vs = Check("", 6000, Structured{ChapterWords: rng})
	if len(vs) != 0 {
		t.Errorf("at max should be in range, got %+v", vs)
	}
}

func TestCheck_ChapterWordsSlightlyBelow(t *testing.T) {
	// actual 2401 → deviation = 599/3000 = 0.1996... < 20% → warning
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check("", 2401, Structured{ChapterWords: rng})
	if len(vs) != 1 || vs[0].Rule != "chapter_words" {
		t.Fatalf("expected 1 chapter_words violation, got %+v", vs)
	}
	if vs[0].Severity != SeverityWarning {
		t.Errorf("severity=%s, want warning at <20%%", vs[0].Severity)
	}
	if vs[0].Deviation >= ChapterWordsDeviationThreshold {
		t.Errorf("deviation=%f should be < %f", vs[0].Deviation, ChapterWordsDeviationThreshold)
	}
}

func TestCheck_ChapterWordsAtThreshold(t *testing.T) {
	// actual 2400 → deviation = 600/3000 = 0.2 == 20% → error（>= threshold）
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check("", 2400, Structured{ChapterWords: rng})
	if len(vs) != 1 || vs[0].Severity != SeverityError {
		t.Errorf("expected error at 20%% threshold, got %+v", vs)
	}
}

func TestCheck_ChapterWordsAboveMax(t *testing.T) {
	// actual 7200 → deviation = 1200/6000 = 0.2 == 20% → error
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check("", 7200, Structured{ChapterWords: rng})
	if len(vs) != 1 || vs[0].Severity != SeverityError {
		t.Errorf("expected error at 20%% above max, got %+v", vs)
	}
	if vs[0].Actual != 7200 {
		t.Errorf("actual=%v, want 7200", vs[0].Actual)
	}
}

func TestCheck_ChapterWordsSlightlyAbove(t *testing.T) {
	// actual 7199 → deviation = 1199/6000 ≈ 0.19983 < 20% → warning
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check("", 7199, Structured{ChapterWords: rng})
	if len(vs) != 1 || vs[0].Severity != SeverityWarning {
		t.Errorf("expected warning slightly above max, got %+v", vs)
	}
}

func TestCheck_AutoWordCount(t *testing.T) {
	// wordCount = -1 时由 checker 自行计算
	text := strings.Repeat("汉", 2500) // 2500 个汉字
	rng := &WordRange{Min: 3000, Max: 6000}
	vs := Check(text, -1, Structured{ChapterWords: rng})
	if len(vs) != 1 || vs[0].Rule != "chapter_words" {
		t.Fatalf("expected 1 chapter_words violation, got %+v", vs)
	}
	if vs[0].Actual != 2500 {
		t.Errorf("auto wordCount=%v, want 2500", vs[0].Actual)
	}
	if vs[0].Actual != utf8.RuneCountInString(text) {
		t.Errorf("auto count mismatch: %v vs rune count %d", vs[0].Actual, utf8.RuneCountInString(text))
	}
}

func TestCheck_MultipleRulesAtOnce(t *testing.T) {
	text := "他不禁——又不禁——离去。"
	rng := &WordRange{Min: 3000, Max: 6000}
	s := Structured{
		ChapterWords:   rng,
		ForbiddenChars: []string{"——"},
		FatigueWords:   map[string]int{"不禁": 1},
	}
	vs := Check(text, 10, s)

	// 应同时触发三类：forbidden_chars + fatigue_words + chapter_words
	rules := map[string]bool{}
	for _, v := range vs {
		rules[v.Rule] = true
	}
	if !rules["forbidden_chars"] || !rules["fatigue_words"] || !rules["chapter_words"] {
		t.Errorf("expected all three rules triggered, got %+v", rules)
	}
}

func TestCheck_FatigueZeroLimitSkipped(t *testing.T) {
	// limit=0 是非法值，应Bỏ qua整条规则（parser 也会过滤，这里防御）
	text := "不禁不禁不禁"
	vs := Check(text, -1, Structured{
		FatigueWords: map[string]int{"不禁": 0},
	})
	if len(vs) != 0 {
		t.Errorf("limit=0 should be skipped, got %+v", vs)
	}
}

func TestCheck_EmptyTargetsSkipped(t *testing.T) {
	// Rỗng字符串目标不应导致 false positive
	vs := Check("任何文本", -1, Structured{
		ForbiddenChars:   []string{""},
		ForbiddenPhrases: []string{""},
		FatigueWords:     map[string]int{"": 1},
	})
	if len(vs) != 0 {
		t.Errorf("empty targets should be skipped, got %+v", vs)
	}
}
