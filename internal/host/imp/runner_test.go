package imp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// scriptedLLM 按调用顺序返回不同响应：第一次 foundation envelope，之后每次 analyzer envelope。
type scriptedLLM struct {
	responses []string
	calls     atomic.Int32
}

func (s *scriptedLLM) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := int(s.calls.Add(1)) - 1
	if idx >= len(s.responses) {
		return nil, fmt.Errorf("scriptedLLM exhausted at call %d", idx+1)
	}
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:      agentcore.RoleAssistant,
			Content:   []agentcore.ContentBlock{agentcore.TextBlock(s.responses[idx])},
			Timestamp: time.Now(),
		},
	}, nil
}

func TestRunner_FullImport(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "novel.txt")
	body := strings.Repeat("正文段落，足够字数以通过 LoadChapterContent 校验。\n", 30)
	content := "第一章 初遇\n" + body + "\n第二章 循迹\n" + body
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "out"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("runner-test", 0); err != nil {
		t.Fatal(err)
	}

	llm := &scriptedLLM{responses: []string{
		validEnvelope,
		validAnalyzerEnvelope,
		validAnalyzerEnvelope,
	}}
	deps := Deps{
		Store:      st,
		CommitTool: tools.NewCommitChapterTool(st),
		LLM:        llm,
		Prompts: Prompts{
			Foundation: "foundation prompt with ${chapter_count}",
			Analyzer:   "analyzer prompt",
		},
	}

	events, err := Run(context.Background(), deps, Options{SourcePath: src})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var stages []Stage
	var lastErr error
	for ev := range events {
		stages = append(stages, ev.Stage)
		if ev.Err != nil {
			lastErr = ev.Err
		}
	}
	if lastErr != nil {
		t.Fatalf("import errored: %v", lastErr)
	}
	if stages[len(stages)-1] != StageDone {
		t.Errorf("last stage: %v, want done; full: %v", stages[len(stages)-1], stages)
	}

	prog, _ := st.Progress.Load()
	if len(prog.CompletedChapters) != 2 {
		t.Errorf("completed chapters: %v", prog.CompletedChapters)
	}
	if llm.calls.Load() != 3 {
		t.Errorf("expected 3 LLM calls (1 foundation + 2 chapters), got %d", llm.calls.Load())
	}
}

func TestRunner_SkipsAlreadyCompletedChapters(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "novel.txt")
	body := strings.Repeat("正文段落。\n", 30)
	content := "第一章 a\n" + body + "\n第二章 b\n" + body
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "out"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("skip-test", 0); err != nil {
		t.Fatal(err)
	}

	// 第一次导入：3 次 LLM 调用 (foundation + 2 chapters)
	llm := &scriptedLLM{responses: []string{
		validEnvelope,
		validAnalyzerEnvelope,
		validAnalyzerEnvelope,
	}}
	deps := Deps{
		Store:      st,
		CommitTool: tools.NewCommitChapterTool(st),
		LLM:        llm,
		Prompts:    Prompts{Foundation: "x", Analyzer: "x"},
	}
	events, err := Run(context.Background(), deps, Options{SourcePath: src})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if llm.calls.Load() != 3 {
		t.Fatalf("first import: want 3 calls, got %d", llm.calls.Load())
	}

	// 第二次导入相同文件：foundation 已存在 → 0 次 LLM；章节已完成 → 0 次 LLM
	llm2 := &scriptedLLM{responses: []string{}} // 任何 LLM 调用都会失败
	deps.LLM = llm2
	events2, err := Run(context.Background(), deps, Options{SourcePath: src})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events2 {
		if ev.Err != nil {
			t.Fatalf("re-import errored: %v", ev.Err)
		}
	}
	if llm2.calls.Load() != 0 {
		t.Errorf("re-import should make 0 LLM calls, got %d", llm2.calls.Load())
	}
}

func TestRunner_ResumeFromSkipsFoundation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "novel.txt")
	body := strings.Repeat("正文。\n", 30)
	content := "第一章 a\n" + body + "\n第二章 b\n" + body
	_ = os.WriteFile(src, []byte(content), 0o644)

	st := store.NewStore(filepath.Join(dir, "out"))
	_ = st.Init()
	_ = st.Progress.Init("resume-test", 0)
	// 预置 foundation
	fr, _ := parseFoundationOutput(validEnvelope, 2)
	if err := PersistFoundation(context.Background(), st, "short", fr); err != nil {
		t.Fatal(err)
	}

	llm := &scriptedLLM{responses: []string{validAnalyzerEnvelope, validAnalyzerEnvelope}}
	deps := Deps{
		Store:      st,
		CommitTool: tools.NewCommitChapterTool(st),
		LLM:        llm,
		Prompts:    Prompts{Foundation: "x", Analyzer: "x"},
	}
	events, err := Run(context.Background(), deps, Options{SourcePath: src, ResumeFrom: 1})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("err: %v", ev.Err)
		}
	}
	if llm.calls.Load() != 2 {
		t.Errorf("want 2 chapter LLM calls (foundation skipped), got %d", llm.calls.Load())
	}
}
