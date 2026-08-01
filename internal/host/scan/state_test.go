package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNextAction 验证状态机的所有分支。
func TestNextAction(t *testing.T) {
	tests := []struct {
		name  string
		facts Facts
		want  Action
	}{
		{
			name:  "空库 → fetch",
			facts: Facts{},
			want:  ActionFetch,
		},
		{
			name:  "库就绪但未解析 → parse",
			facts: Facts{LibraryReady: true},
			want:  ActionParse,
		},
		{
			name:  "已解析但未分析 → analyze",
			facts: Facts{LibraryReady: true, Parsed: true},
			want:  ActionAnalyze,
		},
		{
			name:  "已分析但选题未就绪 → topic",
			facts: Facts{LibraryReady: true, Parsed: true, Analyzed: true},
			want:  ActionTopic,
		},
		{
			name:  "全部完成 → done",
			facts: Facts{LibraryReady: true, Parsed: true, Analyzed: true, Topiced: true},
			want:  ActionDone,
		},
		{
			// 终态优先：上游工件因 prompt 版本升级失鲜也不追溯重做，
			// 否则每次升级都要让用户重付一遍全部调用。
			name:  "选题已就绪但上游失鲜 → 仍是 done",
			facts: Facts{LibraryReady: true, Parsed: false, Analyzed: false, Topiced: true},
			want:  ActionDone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextAction(tt.facts)
			if got != tt.want {
				t.Errorf("NextAction() = %v, want %v", got, tt.want)
			}
		})
	}
}

// newTestLibrary 建一个带完整身份（_meta.json + sources/ 快照）的扫榜库。
func newTestLibrary(t *testing.T) *Library {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "qidian_月票榜_20260101")
	l, err := InitLibrary(dir, "qidian", "月票榜", "20260101", 1)
	if err != nil {
		t.Fatalf("InitLibrary: %v", err)
	}
	if err := l.SaveSources([]Source{{Platform: "qidian", RankName: "月票榜", Raw: "榜单原文", Origin: "paste"}}); err != nil {
		t.Fatalf("SaveSources: %v", err)
	}
	return l
}

func testEntries() []Entry {
	return []Entry{
		{Rank: 1, Title: "书甲", Author: "作者甲", Category: "玄幻", Description: "简介甲", Stats: "10万"},
		{Rank: 2, Title: "书乙", Author: "作者乙", Category: "都市", Description: "简介乙", Stats: "8万"},
	}
}

// writeTestEntries 落一份新鲜的 entries.json，返回其质量报告。
func writeTestEntries(t *testing.T, l *Library) cleanedEntries {
	t.Helper()
	entries, quality := CleanEntries(testEntries(), "qidian")
	payload := cleanedEntries{
		Entries: sortByRank(entries), Quality: quality,
		SourceDigest: testSourceDigest(t, l),
	}
	digest := entriesInputDigest(payload.Entries, "20260101", "qidian")
	if err := writeArtifact(l, fileEntries, digest, payload); err != nil {
		t.Fatalf("writeArtifact entries: %v", err)
	}
	return payload
}

func testSourceDigest(t *testing.T, l *Library) string {
	t.Helper()
	sources, err := l.LoadSources()
	if err != nil {
		t.Fatal(err)
	}
	return sourcesDigest(sources)
}

// TestLoadStateDigestChain 验证 LoadState 沿 digest 链逐级判定新鲜度，
// 且上游工件被改动时下游自动失效——这是「续跑靠重算而非 from=N」的核心保证。
func TestLoadStateDigestChain(t *testing.T) {
	l := newTestLibrary(t)

	// 只有身份：应该要求解析。
	f, err := LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !f.LibraryReady || f.Parsed {
		t.Fatalf("身份就绪后应 LibraryReady 且未 Parsed，得到 %+v", f)
	}
	if got := NextAction(f); got != ActionParse {
		t.Errorf("NextAction = %v, want %v", got, ActionParse)
	}

	// 落条目：应该要求分析，且条目数/稀疏标记从工件读出。
	payload := writeTestEntries(t, l)
	f, err = LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !f.Parsed {
		t.Fatal("落 entries.json 后应 Parsed")
	}
	if f.ValidEntries != 2 {
		t.Errorf("ValidEntries = %d, want 2", f.ValidEntries)
	}
	if !f.Sparse {
		t.Error("2 条有效条目远低于阈值，应标记 Sparse")
	}
	if got := NextAction(f); got != ActionAnalyze {
		t.Errorf("NextAction = %v, want %v", got, ActionAnalyze)
	}

	// 落趋势：应该要求选题。
	entRaw, err := l.readBytes(fileEntries)
	if err != nil {
		t.Fatal(err)
	}
	analysis := Analysis{Verdict: "总评", Trends: []Trend{{Title: "趋势", Evidence: []string{"书甲"}, Reading: "解读"}}}
	if err := writeArtifact(l, fileAnalysis, analysisInputDigest(entRaw), analysis); err != nil {
		t.Fatal(err)
	}
	f, err = LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !f.Analyzed {
		t.Fatal("落 analysis.json 后应 Analyzed")
	}
	if got := NextAction(f); got != ActionTopic {
		t.Errorf("NextAction = %v, want %v", got, ActionTopic)
	}

	// 落选题：终态。
	anaRaw, err := l.readBytes(fileAnalysis)
	if err != nil {
		t.Fatal(err)
	}
	report := TopicReport{Ideas: []TopicIdea{{Direction: "方向", Why: "理由", Feasibility: FeasibilityMedium}}}
	if err := writeArtifact(l, fileTopic, topicInputDigest(anaRaw), report); err != nil {
		t.Fatal(err)
	}
	f, err = LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := NextAction(f); got != ActionDone {
		t.Errorf("NextAction = %v, want %v", got, ActionDone)
	}

	// 重写条目（内容不同 → digest 不同）：趋势与选题都必须失鲜。
	changed := append(payload.Entries, Entry{Rank: 3, Title: "书丙", Author: "作者丙"})
	if err := writeArtifact(l, fileEntries, entriesInputDigest(changed, "20260101", "qidian"), cleanedEntries{
		Entries: changed, Quality: payload.Quality, SourceDigest: payload.SourceDigest,
	}); err != nil {
		t.Fatal(err)
	}
	f, err = LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if f.Analyzed {
		t.Error("条目变化后趋势工件必须失鲜")
	}
	if got := NextAction(f); got != ActionAnalyze {
		t.Errorf("NextAction = %v, want %v（条目变化应重跑趋势）", got, ActionAnalyze)
	}
}

// TestLoadStateHalfIdentity 验证半初始化的库（_meta.json 在但 sources/ 空）
// 回到 fetch 而不是被误判成可续跑——否则会拿着空数据源往下跑。
func TestLoadStateHalfIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "qidian_月票榜_20260101")
	l, err := InitLibrary(dir, "qidian", "月票榜", "20260101", 0)
	if err != nil {
		t.Fatalf("InitLibrary: %v", err)
	}

	f, err := LoadState(l, Options{})
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if f.LibraryReady {
		t.Error("sources/ 为空时不应判定 LibraryReady")
	}
	if got := NextAction(f); got != ActionFetch {
		t.Errorf("NextAction = %v, want %v", got, ActionFetch)
	}
}

func TestLoadStateRejectsDifferentProvidedSources(t *testing.T) {
	l := newTestLibrary(t)
	if _, err := LoadState(l, Options{PastedText: "另一份榜单原文", Platform: "qidian", RankName: "月票榜"}); err == nil {
		t.Fatal("同名同日库收到不同数据源时不得静默复用")
	}
}

func TestLoadStateReportsBrokenSourcesSnapshot(t *testing.T) {
	l := newTestLibrary(t)
	if err := os.RemoveAll(l.path(dirSources)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path(dirSources), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(l, Options{})
	if err == nil || !strings.Contains(err.Error(), "读取扫榜库数据源快照") {
		t.Fatalf("损坏的 sources/ 应显式报错，得到 %v", err)
	}
}

func TestReplaceSourcesInvalidatesParsedArtifacts(t *testing.T) {
	l := newTestLibrary(t)
	writeTestEntries(t, l)
	if err := l.ReplaceSources([]Source{{Platform: "qidian", RankName: "月票榜", Raw: "全新榜单内容", Origin: "paste"}}); err != nil {
		t.Fatal(err)
	}
	f, err := LoadState(l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.LibraryReady || f.Parsed {
		t.Fatalf("换源后应保留库身份并使旧 entries 失效: %+v", f)
	}
}

// TestLoadStateTamperedEntries 验证手工改过的 entries.json 被当场拦下：
// 载荷与自报 InputDigest 不一致时报错，而不是拿着不可信数据继续分析。
func TestLoadStateTamperedEntries(t *testing.T) {
	l := newTestLibrary(t)
	payload := writeTestEntries(t, l)

	// 保留原 digest，但把载荷改掉。
	tampered := cleanedEntries{
		Entries:      []Entry{{Rank: 1, Title: "伪造", Author: "伪造"}},
		Quality:      payload.Quality,
		SourceDigest: payload.SourceDigest,
	}
	if err := writeArtifact(l, fileEntries, entriesInputDigest(payload.Entries, "20260101", "qidian"), tampered); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(l, Options{}); err == nil {
		t.Fatal("载荷与 InputDigest 不一致时 LoadState 必须报错")
	}
}

// TestLoadStateSchemaVersionMismatch 验证 schema 版本不匹配显式报错，不静默重跑。
func TestLoadStateSchemaVersionMismatch(t *testing.T) {
	l := newTestLibrary(t)
	writeTestEntries(t, l)

	// 手工把 schema_version 改成未来版本。
	bad := `{"schema_version": 999, "input_digest": "x", "payload": {"entries": [], "quality": {}}}`
	if err := os.WriteFile(l.path(fileEntries), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(l, Options{}); err == nil {
		t.Fatal("schema 版本不匹配时 LoadState 必须报错")
	}
}

// TestResumeSummary 验证恢复提示随进度变化，完成后为空串。
func TestResumeSummary(t *testing.T) {
	if got := ResumeSummary(filepath.Join(t.TempDir(), "不存在")); got != "" {
		t.Errorf("不存在的库应返回空串，得到 %q", got)
	}

	l := newTestLibrary(t)
	if got := ResumeSummary(l.Dir()); got == "" {
		t.Error("未完成的库应有恢复提示")
	}

	writeTestEntries(t, l)
	entRaw, _ := l.readBytes(fileEntries)
	analysis := Analysis{Verdict: "总评"}
	if err := writeArtifact(l, fileAnalysis, analysisInputDigest(entRaw), analysis); err != nil {
		t.Fatal(err)
	}
	anaRaw, _ := l.readBytes(fileAnalysis)
	if err := writeArtifact(l, fileTopic, topicInputDigest(anaRaw), TopicReport{}); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSummary(l.Dir()); got != "" {
		t.Errorf("已完成的库应返回空串，得到 %q", got)
	}
}
