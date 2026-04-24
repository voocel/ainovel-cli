package imp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type mockLLM struct {
	out string
	err error
	got []agentcore.Message
}

func (m *mockLLM) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.got = msgs
	if m.err != nil {
		return nil, m.err
	}
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:      agentcore.RoleAssistant,
			Content:   []agentcore.ContentBlock{agentcore.TextBlock(m.out)},
			Timestamp: time.Now(),
		},
	}, nil
}

const validEnvelope = `=== PREMISE ===
# 测试书名

## 题材和基调
现代都市悬疑

## 核心冲突
新闻记者追查连环失踪案

## 主角目标
找出真凶并自证清白

## 结局方向
真相大白，主角抉择

## 写作禁区
血腥猎奇，跳脱现实

## 差异化卖点
- 双线叙事
- 女性视角

## 差异化钩子
失踪者全部姓"陈"

## 核心兑现承诺
追完能体验完整悬疑解谜

=== CHARACTERS ===
[
  {"name":"林晚","role":"主角","description":"独立记者","arc":"前期被动追案，后期主动出击","traits":["敏锐","固执"]},
  {"name":"陈沉","role":"反派","description":"幕后凶手","arc":"前期隐蔽，后期暴露","traits":["冷静","残忍"]}
]

=== WORLD_RULES ===
[
  {"category":"society","rule":"现代都市背景，警力体系完备","boundary":"不超自然"}
]

=== OUTLINE ===
[
  {"chapter":1,"title":"初遇","core_event":"林晚收到匿名爆料","hook":"线索指向陈姓家族","scenes":["编辑部","咖啡馆"]},
  {"chapter":2,"title":"循迹","core_event":"林晚走访失踪者家属","hook":"发现共同祭品符号","scenes":["旧宅","档案馆"]}
]
`

func TestReverseFoundation_ParsesValid(t *testing.T) {
	llm := &mockLLM{out: validEnvelope}
	chapters := []Chapter{
		{Title: "初遇", Content: "林晚翻开匿名信..."},
		{Title: "循迹", Content: "她敲响那栋旧宅的门..."},
	}
	got, err := ReverseFoundation(context.Background(), llm, "system prompt with ${chapter_count}", chapters)
	if err != nil {
		t.Fatalf("ReverseFoundation: %v", err)
	}
	if !strings.HasPrefix(got.Premise, "# 测试书名") {
		t.Errorf("premise head: %q", got.Premise[:20])
	}
	if len(got.Characters) != 2 || got.Characters[0].Name != "林晚" {
		t.Errorf("characters wrong: %+v", got.Characters)
	}
	if len(got.Outline) != 2 || got.Outline[1].Chapter != 2 {
		t.Errorf("outline wrong: %+v", got.Outline)
	}
	if !strings.Contains(llm.got[0].TextContent(), "with 2") {
		t.Errorf("system prompt expected ${chapter_count}=2 substituted, got: %q",
			llm.got[0].TextContent())
	}
	if !strings.Contains(llm.got[1].TextContent(), "林晚翻开匿名信") {
		t.Errorf("user prompt should contain chapter 1 content")
	}
}

func TestReverseFoundation_RejectsLengthMismatch(t *testing.T) {
	llm := &mockLLM{out: validEnvelope}
	chapters := []Chapter{
		{Title: "ch1", Content: "..."},
		{Title: "ch2", Content: "..."},
		{Title: "ch3", Content: "..."},
	}
	_, err := ReverseFoundation(context.Background(), llm, "x", chapters)
	if err == nil || !strings.Contains(err.Error(), "outline length mismatch") {
		t.Fatalf("want length-mismatch error, got %v", err)
	}
}

func TestReverseFoundation_MissingTagFails(t *testing.T) {
	llm := &mockLLM{out: "=== PREMISE ===\n# x\n"}
	_, err := ReverseFoundation(context.Background(), llm,
		"x", []Chapter{{Title: "a", Content: "b"}})
	if err == nil || !strings.Contains(err.Error(), "missing required tags") {
		t.Fatalf("want missing-tags error, got %v", err)
	}
}

func TestParseFoundation_FencedJSONStripped(t *testing.T) {
	src := strings.ReplaceAll(validEnvelope,
		`=== CHARACTERS ===
[`,
		"=== CHARACTERS ===\n```json\n[",
	)
	src = strings.ReplaceAll(src, `]

=== WORLD_RULES ===`, "]\n```\n\n=== WORLD_RULES ===")
	got, err := parseFoundationOutput(src, 2)
	if err != nil {
		t.Fatalf("fenced parse: %v", err)
	}
	if len(got.Characters) != 2 {
		t.Errorf("characters: %+v", got.Characters)
	}
}

func TestPersistFoundation_PromotesPhaseToWriting(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Progress.Init("import-test", 0); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	fr := mustParse(t, validEnvelope, 2)
	if err := PersistFoundation(context.Background(), st, domain.PlanningTierShort, fr); err != nil {
		t.Fatalf("PersistFoundation: %v", err)
	}

	prog, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if prog.Phase != domain.PhaseWriting {
		t.Errorf("phase: got %q want writing", prog.Phase)
	}
	if prog.TotalChapters != 2 {
		t.Errorf("total chapters: %d", prog.TotalChapters)
	}
	if prog.NovelName != "测试书名" {
		t.Errorf("novel name: %q", prog.NovelName)
	}
	if got := st.FoundationMissing(); len(got) != 0 {
		t.Errorf("foundation should be complete, missing: %v", got)
	}
}

func mustParse(t *testing.T, raw string, expect int) *FoundationResult {
	t.Helper()
	fr, err := parseFoundationOutput(raw, expect)
	if err != nil {
		t.Fatalf("parse helper: %v", err)
	}
	return fr
}
