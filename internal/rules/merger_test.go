package rules

import (
	"reflect"
	"strings"
	"testing"
)

// makeParsed 是测试辅助：构造一个 Parsed，省略冗长字段。
func makeParsed(source string, kind SourceKind, s Structured, pref string) Parsed {
	return Parsed{Source: source, Kind: kind, Structured: s, Preference: pref}
}

func TestMerge_Empty(t *testing.T) {
	b := Merge(nil)
	if !b.IsEmpty() {
		t.Errorf("merge nil should be empty, got %+v", b)
	}
	if len(b.Sources) != 0 || len(b.Conflicts) != 0 {
		t.Errorf("merge nil should have no sources/conflicts, got %+v", b)
	}
}

func TestMerge_NearestWinsScalar(t *testing.T) {
	// default 说 chapter_words: 3000-6000，project 说 chapter_words: 4000-8000
	// 期望：项目优先
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{
			ChapterWords: &WordRange{Min: 3000, Max: 6000},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			ChapterWords: &WordRange{Min: 4000, Max: 8000},
		}, ""),
	}
	b := Merge(layers)
	if b.Structured.ChapterWords == nil || b.Structured.ChapterWords.Min != 4000 || b.Structured.ChapterWords.Max != 8000 {
		t.Errorf("project should win, got %+v", b.Structured.ChapterWords)
	}
	// Nhất quán冲突应被识别
	if !hasConflict(b.Conflicts, ConflictFieldConflict, "chapter_words") {
		t.Errorf("expected field_conflict for chapter_words, got %+v", b.Conflicts)
	}
}

func TestMerge_NoConflictWhenEqual(t *testing.T) {
	// 两层都声明同一字段，但值完全一致 → 不算冲突
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{
			ChapterWords:   &WordRange{Min: 3000, Max: 6000},
			ForbiddenChars: []string{"——"},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			ChapterWords:   &WordRange{Min: 3000, Max: 6000},
			ForbiddenChars: []string{"——"},
		}, ""),
	}
	b := Merge(layers)
	for _, c := range b.Conflicts {
		if c.Kind == ConflictFieldConflict {
			t.Errorf("same value should not produce field_conflict, got %+v", c)
		}
	}
}

func TestMerge_NearestWinsList(t *testing.T) {
	// global ["——"], project ["（"]，期望项目生效，且报冲突
	layers := []Parsed{
		makeParsed("global.md", SourceGlobal, Structured{
			ForbiddenChars: []string{"——"},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			ForbiddenChars: []string{"（"},
		}, ""),
	}
	b := Merge(layers)
	if !reflect.DeepEqual(b.Structured.ForbiddenChars, []string{"（"}) {
		t.Errorf("expected project list, got %v", b.Structured.ForbiddenChars)
	}
	if !hasConflict(b.Conflicts, ConflictFieldConflict, "forbidden_chars") {
		t.Errorf("expected field_conflict for forbidden_chars, got %+v", b.Conflicts)
	}
}

func TestMerge_FatigueWordsMergeByKey(t *testing.T) {
	// genre fatigue {不禁:1}; project fatigue {竟然:2} → 按词合并，避免用户只Mới增一词时丢失Mặc định规则
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{
			FatigueWords: map[string]int{"不禁": 1},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			FatigueWords: map[string]int{"竟然": 2},
		}, ""),
	}
	b := Merge(layers)
	want := map[string]int{"不禁": 1, "竟然": 2}
	if !reflect.DeepEqual(b.Structured.FatigueWords, want) {
		t.Errorf("fatigue_words should merge by key, got %v want %v", b.Structured.FatigueWords, want)
	}
	if hasConflict(b.Conflicts, ConflictFieldConflict, "fatigue_words") {
		t.Errorf("different fatigue_words keys should not produce field-level conflict, got %+v", b.Conflicts)
	}
}

func TestMerge_FatigueWordsNearestWinsSameKey(t *testing.T) {
	// 同一疲劳词多来源声明不同阈值 → 就近优先，并只针对该词报冲突
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{
			FatigueWords: map[string]int{"不禁": 1, "然而": 2},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			FatigueWords: map[string]int{"不禁": 3, "其实": 1},
		}, ""),
	}
	b := Merge(layers)
	want := map[string]int{"不禁": 3, "然而": 2, "其实": 1}
	if !reflect.DeepEqual(b.Structured.FatigueWords, want) {
		t.Errorf("fatigue_words should merge with nearest value for same key, got %v want %v", b.Structured.FatigueWords, want)
	}
	if !hasConflict(b.Conflicts, ConflictFieldConflict, "fatigue_words.不禁") {
		t.Errorf("expected per-word field_conflict for 不禁, got %+v", b.Conflicts)
	}
}

func TestMerge_PreservesUntouchedFields(t *testing.T) {
	// 低优先级声明字段 A；高优先级只声明字段 B → A 应保留
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{
			ForbiddenChars: []string{"——"},
		}, ""),
		makeParsed("project.md", SourceProject, Structured{
			Genre: "xianxia",
		}, ""),
	}
	b := Merge(layers)
	if b.Structured.Genre != "xianxia" {
		t.Errorf("genre missing, got %+v", b.Structured)
	}
	if !reflect.DeepEqual(b.Structured.ForbiddenChars, []string{"——"}) {
		t.Errorf("forbidden_chars from default should be preserved, got %v", b.Structured.ForbiddenChars)
	}
}

func TestMerge_MarkdownConcatenated(t *testing.T) {
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{}, "Mặc định偏好Chính văn"),
		makeParsed("project.md", SourceProject, Structured{}, "项目偏好Chính văn"),
	}
	b := Merge(layers)
	if !strings.Contains(b.Preferences, "Mặc định偏好Chính văn") {
		t.Errorf("default body missing: %q", b.Preferences)
	}
	if !strings.Contains(b.Preferences, "项目偏好Chính văn") {
		t.Errorf("project body missing: %q", b.Preferences)
	}
	// 顺序：default 在前，project 在后
	di := strings.Index(b.Preferences, "Mặc định偏好Chính văn")
	pi := strings.Index(b.Preferences, "项目偏好Chính văn")
	if di >= pi {
		t.Errorf("default body should appear before project body; default@%d project@%d", di, pi)
	}
	// 来源Tiêu đề
	if !strings.Contains(b.Preferences, "[default] default.md") {
		t.Errorf("source header for default missing: %q", b.Preferences)
	}
	if !strings.Contains(b.Preferences, "[project] project.md") {
		t.Errorf("source header for project missing: %q", b.Preferences)
	}
}

func TestMerge_SkipsEmptyBody(t *testing.T) {
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{}, "   "),
		makeParsed("project.md", SourceProject, Structured{}, "项目Chính văn"),
	}
	b := Merge(layers)
	if strings.Contains(b.Preferences, "[default]") {
		t.Errorf("empty body should not emit source header, got %q", b.Preferences)
	}
	if !strings.Contains(b.Preferences, "项目Chính văn") {
		t.Errorf("project body missing: %q", b.Preferences)
	}
}

func TestMerge_PropagatesParsedConflicts(t *testing.T) {
	// 单Tập tin已有解析期 conflict（如 unknown field），merger 应原样汇总
	parsed := Parsed{
		Source: "project.md",
		Kind:   SourceProject,
		Conflicts: []Conflict{{
			Source: "project.md",
			Kind:   ConflictUnknownField,
			Field:  "secret_x",
			Detail: "Không rõ",
		}},
	}
	b := Merge([]Parsed{parsed})
	if !hasConflict(b.Conflicts, ConflictUnknownField, "secret_x") {
		t.Errorf("expected ConflictUnknownField for secret_x, got %+v", b.Conflicts)
	}
}

func TestMerge_AllSourcesInList(t *testing.T) {
	layers := []Parsed{
		makeParsed("default.md", SourceDefault, Structured{}, ""),
		makeParsed("global.md", SourceGlobal, Structured{}, ""),
		makeParsed("project.md", SourceProject, Structured{}, ""),
	}
	b := Merge(layers)
	want := []string{"default.md", "global.md", "project.md"}
	if !reflect.DeepEqual(b.Sources, want) {
		t.Errorf("sources=%v, want %v", b.Sources, want)
	}
}

// hasConflict Kiểm tra conflicts 中Có czy không存在指定 (Kind, Field) 的条目。
func hasConflict(conflicts []Conflict, kind ConflictKind, field string) bool {
	for _, c := range conflicts {
		if c.Kind == kind && c.Field == field {
			return true
		}
	}
	return false
}
