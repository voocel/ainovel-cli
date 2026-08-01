package rip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBoundaryDigestMismatchTriggersRebind 验证边界表 InputDigest 失配会让 LoadState 判 Bounded=false，
// 触发重新识别边界（而不是复用旧边界表继续）。
func TestBoundaryDigestMismatchTriggersRebind(t *testing.T) {
	tmp := t.TempDir()
	bookName := "test-book"
	libDir := filepath.Join(tmp, bookName)

	// 1. 构造一个完整的拆文库身份（manifest + intent + source）+ 一份边界表
	m := Manifest{
		Version:    librarySchemaVersion,
		BookName:   bookName,
		SourceName: "test.txt",
		Encoding:   "UTF-8",
		Runes:      25000, // 长篇
		RawSHA256:  Digest([]byte("original source")),
	}
	in := Intent{
		Version: librarySchemaVersion,
		Form:    string(FormLong),
	}
	src := []byte("original source")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	l := OpenLibrary(libDir)
	if err := l.writeJSON(fileManifest, m); err != nil {
		t.Fatal(err)
	}
	if err := l.writeJSON(fileIntent, in); err != nil {
		t.Fatal(err)
	}
	if err := l.writeAtomic(sourceRel(), src); err != nil {
		t.Fatal(err)
	}

	// 边界表：InputDigest 绑定「源文件摘要 + 切分指导 + prompt 版本」
	oldDigest := "old-digest-v1"
	b := Boundaries{
		Chapters: []ChapterSpan{{Number: 1, Start: 0, End: 100, Title: "第一章"}},
	}
	if err := writeArtifact(l, fileBoundaries, oldDigest, b); err != nil {
		t.Fatal(err)
	}

	// 2. LoadState 期待的 digest 与落盘的不匹配 → Bounded 应为 false
	opts := Options{}
	facts, err := LoadState(l, opts)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	newDigest := boundInputDigest(Digest(src), "", boundPromptVersion)
	if newDigest == oldDigest {
		t.Fatal("test setup error: new digest should differ from old")
	}
	if facts.Bounded {
		t.Errorf("Bounded = true when digest mismatches, want false to trigger re-bind")
	}
	if NextAction(facts) != ActionBound {
		t.Errorf("NextAction = %q, want ActionBound when boundary digest stale", NextAction(facts))
	}
}

// TestChapterArtifactResume 验证逐章摘要的断点续跑：前 N 章已有新鲜工件，LoadState 读出的
// SummarizedChapters 应为 N，NextAction 仍为 ActionSummarize，resume 会从第 N+1 章继续。
func TestChapterArtifactResume(t *testing.T) {
	tmp := t.TempDir()
	bookName := "resume-test"
	libDir := filepath.Join(tmp, bookName)

	// 准备完整拆文库：manifest + intent + source + boundaries
	m := Manifest{
		Version:    librarySchemaVersion,
		BookName:   bookName,
		SourceName: "test.txt",
		Encoding:   "UTF-8",
		Runes:      30000,
		RawSHA256:  Digest([]byte("source text")),
	}
	in := Intent{
		Version:     librarySchemaVersion,
		Form:        string(FormLong),
		AutoConfirm: true, // 跳过预览停靠点
	}
	src := []byte("第一章\n内容1\n第二章\n内容2\n第三章\n内容3\n第四章\n内容4\n第五章\n内容5\n" + strings.Repeat("文本填充。", 100))
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	l := OpenLibrary(libDir)
	if err := l.writeJSON(fileManifest, m); err != nil {
		t.Fatal(err)
	}
	if err := l.writeJSON(fileIntent, in); err != nil {
		t.Fatal(err)
	}
	if err := l.writeAtomic(sourceRel(), src); err != nil {
		t.Fatal(err)
	}

	// 边界表：5 章，字节范围必须在 src 实际长度内
	srcLen := len(src)
	boundIdentity := boundInputDigest(Digest(src), "", boundPromptVersion)
	b := Boundaries{
		Chapters: []ChapterSpan{
			{Number: 1, Start: 0, End: srcLen / 5, Title: "第一章"},
			{Number: 2, Start: srcLen / 5, End: srcLen * 2 / 5, Title: "第二章"},
			{Number: 3, Start: srcLen * 2 / 5, End: srcLen * 3 / 5, Title: "第三章"},
			{Number: 4, Start: srcLen * 3 / 5, End: srcLen * 4 / 5, Title: "第四章"},
			{Number: 5, Start: srcLen * 4 / 5, End: srcLen, Title: "第五章"},
		},
	}
	if err := writeArtifact(l, fileBoundaries, boundIdentity, b); err != nil {
		t.Fatal(err)
	}

	// 长篇需要预览工件，digest 必须绑定 boundaries 工件的原始字节
	boundRaw, err := l.readBytes(fileBoundaries)
	if err != nil {
		t.Fatal(err)
	}
	previewDigest := previewInputDigest(boundRaw)
	preview := Preview{
		Chapters: []PreviewChapter{
			{Chapter: 1, Title: "第一章", Opening: "开篇吸引人", Hooks: []string{"悬念"}, Conflict: "主要冲突", Promise: "精彩继续", Risks: []string{}},
		},
		Verdict:    "proceed",
		Strengths:  []string{"开篇吸引人"},
		Weaknesses: []string{},
	}
	if err := writeArtifact(l, filePreview, previewDigest, preview); err != nil {
		t.Fatal(err)
	}

	// 前 3 章的摘要工件已落盘
	for i := 1; i <= 3; i++ {
		chapterDigest := chapterInputDigest(boundIdentity, summaryPromptVersion, &b, src, i-1) // i-1 是数组索引
		payload := ChapterSummaryPayload{
			BatchStart: i,
			BatchEnd:   i,
			Summary: ChapterSummary{
				Chapter:        i,
				Title:          b.Chapters[i-1].Title,
				Summary:        "这一章讲了...",
				PlotPoints:     []PlotPoint{{ID: "P1", Beat: "事件", Tone: "紧张"}},
				KeyFacts:       []string{"事实"},
				Payoffs:        []string{"爽点"},
				HookType:       "crisis",
				DominantStrand: "quest",
				Characters:     []string{"主角"},
				EmotionArc:     "平静→期待",
				Techniques:     []string{"对比"},
			},
		}
		if err := writeArtifact(l, chapterPath(i), chapterDigest, payload); err != nil {
			t.Fatal(err)
		}
	}

	// LoadState 应识别出前 3 章已摘要，SummarizedChapters=3
	opts := Options{AcceptPreview: true}
	facts, err := LoadState(l, opts)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if facts.SummarizedChapters != 3 {
		t.Errorf("SummarizedChapters = %d, want 3 (resume from chapter 4)", facts.SummarizedChapters)
	}
	if facts.ExpectedChapters != 5 {
		t.Errorf("ExpectedChapters = %d, want 5", facts.ExpectedChapters)
	}
	// NextAction 应为 ActionSummarize（还有第 4、5 章未摘要）
	if NextAction(facts) != ActionSummarize {
		t.Errorf("NextAction = %q, want ActionSummarize to continue from chapter 4", NextAction(facts))
	}
}

// TestValidateSummaryBatchEnforcesTitleFromBoundary 验证批量摘要校验会以边界表的标题为准，
// 模型返回的标题被覆盖（标题是坐标事实，由边界阶段定义，摘要阶段不再复述）。
func TestValidateSummaryBatchEnforcesTitleFromBoundary(t *testing.T) {
	b := &Boundaries{
		Chapters: []ChapterSpan{
			{Number: 1, Title: "第一章 启程", Start: 0, End: 100},
		},
	}
	// 模型返回了拼错的标题
	result := &SummaryBatchResult{
		Chapters: []ChapterSummary{
			{
				Chapter:        1,
				Title:          "第一章 起程", // 拼错
				Summary:        "主角踏上旅途",
				PlotPoints:     []PlotPoint{{ID: "P1", Beat: "出发", Tone: "紧张"}},
				KeyFacts:       []string{"离开家乡"},
				HookType:       "crisis",
				DominantStrand: "quest",
				Characters:     []string{"主角"},
				EmotionArc:     "平静→好奇",
				Techniques:     []string{"白描"},
			},
		},
	}
	// validateSummaryBatch 应成功，并把标题改写为边界表的标题
	if err := validateSummaryBatch(result, b, 0, 1); err != nil {
		t.Fatalf("validateSummaryBatch failed: %v", err)
	}
	if result.Chapters[0].Title != "第一章 启程" {
		t.Errorf("Title after validation = %q, want %q (should be overwritten by boundary)",
			result.Chapters[0].Title, "第一章 启程")
	}
}

// TestFailedChapterSkippedAndCounted 验证失败隔离：单章持久失败会被记录到 failures/，
// SummarizedChapters 跳过它继续（视为已处理），最终 Degraded=true。
func TestFailedChapterSkippedAndCounted(t *testing.T) {
	tmp := t.TempDir()
	bookName := "failed-test"
	libDir := filepath.Join(tmp, bookName)

	// 准备 5 章的拆文库，第 3 章记录为失败
	m := Manifest{
		Version:    librarySchemaVersion,
		BookName:   bookName,
		SourceName: "test.txt",
		Encoding:   "UTF-8",
		Runes:      30000,
		RawSHA256:  Digest([]byte("source")),
	}
	in := Intent{
		Version:     librarySchemaVersion,
		Form:        string(FormLong),
		AutoConfirm: true,
	}
	src := []byte(strings.Repeat("文本填充。", 100))
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	l := OpenLibrary(libDir)
	if err := l.writeJSON(fileManifest, m); err != nil {
		t.Fatal(err)
	}
	if err := l.writeJSON(fileIntent, in); err != nil {
		t.Fatal(err)
	}
	if err := l.writeAtomic(sourceRel(), src); err != nil {
		t.Fatal(err)
	}

	srcLen := len(src)
	boundIdentity := boundInputDigest(Digest(src), "", boundPromptVersion)
	b := Boundaries{
		Chapters: []ChapterSpan{
			{Number: 1, Start: 0, End: srcLen / 5, Title: "第一章"},
			{Number: 2, Start: srcLen / 5, End: srcLen * 2 / 5, Title: "第二章"},
			{Number: 3, Start: srcLen * 2 / 5, End: srcLen * 3 / 5, Title: "第三章"},
			{Number: 4, Start: srcLen * 3 / 5, End: srcLen * 4 / 5, Title: "第四章"},
			{Number: 5, Start: srcLen * 4 / 5, End: srcLen, Title: "第五章"},
		},
	}
	if err := writeArtifact(l, fileBoundaries, boundIdentity, b); err != nil {
		t.Fatal(err)
	}

	// 长篇需要预览工件，digest 必须绑定 boundaries 工件的原始字节
	boundRaw, err := l.readBytes(fileBoundaries)
	if err != nil {
		t.Fatal(err)
	}
	previewDigest := previewInputDigest(boundRaw)
	preview := Preview{
		Chapters: []PreviewChapter{
			{Chapter: 1, Title: "第一章", Opening: "开篇", Hooks: []string{"钩子"}, Conflict: "冲突", Promise: "承诺", Risks: []string{}},
		},
		Verdict:    "proceed",
		Strengths:  []string{"优点"},
		Weaknesses: []string{},
	}
	if err := writeArtifact(l, filePreview, previewDigest, preview); err != nil {
		t.Fatal(err)
	}

	// 第 1、2、4、5 章成功摘要
	for _, i := range []int{1, 2, 4, 5} {
		chapterDigest := chapterInputDigest(boundIdentity, summaryPromptVersion, &b, src, i-1) // i-1 是数组索引
		payload := ChapterSummaryPayload{
			BatchStart: i,
			BatchEnd:   i,
			Summary: ChapterSummary{
				Chapter:        i,
				Title:          b.Chapters[i-1].Title,
				Summary:        "ok",
				PlotPoints:     []PlotPoint{{ID: "P1", Beat: "a", Tone: "紧张"}},
				KeyFacts:       []string{"b"},
				Payoffs:        []string{"c"},
				HookType:       "crisis",
				DominantStrand: "quest",
				Characters:     []string{"角色"},
				EmotionArc:     "平静",
				Techniques:     []string{"手法"},
			},
		}
		if err := writeArtifact(l, chapterPath(i), chapterDigest, payload); err != nil {
			t.Fatal(err)
		}
	}

	// 第 3 章记录为持久失败，标记绑定当前章节输入身份。
	failureDigest := chapterInputDigest(boundIdentity, summaryPromptVersion, &b, src, 2)
	l.writeChapterFailure(3, FailureMeta{
		Stage: "summarizing", Detail: "model timeout", InputDigest: failureDigest,
	}, "raw response")

	// LoadState 应把失败章计入 SummarizedChapters（视为已处理）
	opts := Options{AcceptPreview: true}
	facts, err := LoadState(l, opts)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// 1,2 成功 + 3 失败 + 4,5 成功 = 5 章全部"已处理"
	if facts.SummarizedChapters != 5 {
		t.Errorf("SummarizedChapters = %d, want 5 (including failed chapter 3)", facts.SummarizedChapters)
	}
	if facts.FailedChapters != 1 {
		t.Errorf("FailedChapters = %d, want 1", facts.FailedChapters)
	}
	if !facts.Degraded() {
		t.Error("Degraded() = false, want true when FailedChapters > 0")
	}

	// 重切、改 guidance 或升级摘要契约后，旧失败标记不得继续跳过新输入。
	l.writeChapterFailure(3, FailureMeta{
		Stage: "summarizing", Detail: "old failure", InputDigest: "sha256:stale",
	}, "old response")
	facts, err = LoadState(l, opts)
	if err != nil {
		t.Fatalf("LoadState with stale failure: %v", err)
	}
	if facts.FailedChapters != 0 || facts.SummarizedChapters != 2 {
		t.Fatalf("陈旧失败标记应失效并从第 3 章重试，得到 %+v", facts)
	}
}

func TestApplyIntentOptionsPersistsAmbiguousForm(t *testing.T) {
	l := OpenLibrary(t.TempDir())
	if err := l.writeJSON(fileManifest, Manifest{Version: librarySchemaVersion, BookName: "灰区书"}); err != nil {
		t.Fatal(err)
	}
	if err := l.writeJSON(fileIntent, Intent{Version: librarySchemaVersion}); err != nil {
		t.Fatal(err)
	}
	r := &runner{l: l, opts: Options{Form: string(FormLong)}}
	if err := r.applyIntentOptions(); err != nil {
		t.Fatalf("applyIntentOptions: %v", err)
	}
	in, err := l.LoadIntent()
	if err != nil {
		t.Fatal(err)
	}
	if in.Form != string(FormLong) {
		t.Fatalf("灰区裁定未持久化，got %q", in.Form)
	}
	if got := resolvedForm(in, Options{AcceptPreview: true}); got != FormLong {
		t.Fatalf("黄金三章放行重入应保留 long，got %q", got)
	}
}
