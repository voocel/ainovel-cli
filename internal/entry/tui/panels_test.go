package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestRenderTopBarShowsVersion(t *testing.T) {
	out := renderTopBar(host.UISnapshot{
		Provider:  "openrouter",
		ModelName: "test-model",
		BookTitle: "测试小说",
	}, 120, "", "v1.2.3")
	if !strings.Contains(out, "ainovel-cli v1.2.3") {
		t.Fatalf("top bar missing version: %q", out)
	}
}

func TestRenderDetailContentShowsSynopsis(t *testing.T) {
	out := ansi.Strip(renderDetailContent(host.UISnapshot{Synopsis: "少年在永夜中寻找黎明。"}, 40))
	if !strings.Contains(out, "简介") || !strings.Contains(out, "少年在永夜中寻找黎明。") {
		t.Fatalf("detail panel missing synopsis: %q", out)
	}
}

func TestSameDetailSnapshotDetectsOutlineStateChanges(t *testing.T) {
	base := host.UISnapshot{Outline: []host.OutlineSnapshot{{Chapter: 1, Title: "第一章"}}}
	if !sameDetailSnapshot(base, base) {
		t.Fatal("相同详情不应触发重建")
	}
	changed := base
	changed.InProgressChapter = 1
	if sameDetailSnapshot(base, changed) {
		t.Fatal("章节状态变化必须触发详情重建")
	}
}

func TestRenderErrorEventKeepsOneLineSummary(t *testing.T) {
	out := ansi.Strip(renderEventLine(host.Event{
		Time:     time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Category: "ERROR",
		Summary:  "commit_chapter 参数错误：" + strings.Repeat("秦越在材料中发现线索", 20),
	}, 60, 0))
	if strings.Contains(out, "\n") {
		t.Fatalf("ERROR 事件应保持单行摘要，got %q", out)
	}
	if !strings.HasSuffix(out, "...") {
		t.Fatalf("超宽 ERROR 摘要应在 TUI 截断，got %q", out)
	}
}

func TestRenderRunningModelShowsStateAndElapsed(t *testing.T) {
	out := ansi.Strip(renderEventLine(host.Event{
		ID:       "model-1",
		Time:     time.Now().Add(-65 * time.Second),
		Category: "MODEL",
		Summary:  "思考中",
		Depth:    1,
	}, 60, 0))
	if !strings.Contains(out, "思考中") || !strings.Contains(out, "(1m5s)") {
		t.Fatalf("进行中的模型响应应显示状态与实时耗时，got %q", out)
	}
}

// 模型响应只在等待时占一行：成功返回即移出事件流，随后的同 ID 更新落到正确的行；失败保留。
func TestFinishedModelResponseLeavesTheEventStream(t *testing.T) {
	m := Model{eventIndex: map[string]int{}}
	start := time.Now()
	m.applyEventProjection(host.Event{ID: "model-1", Time: start, Category: "MODEL", Summary: "等待模型", Depth: 1})
	m.applyEventProjection(host.Event{ID: "model-1", Time: start, FinishedAt: start.Add(time.Second), Category: "MODEL", Summary: "模型响应", Depth: 1})
	m.applyEventProjection(host.Event{ID: "tool-1", Time: start, Category: "TOOL", Summary: "draft_chapter", Depth: 1})
	m.applyEventProjection(host.Event{ID: "model-2", Time: start, Category: "MODEL", Summary: "等待模型", Depth: 1})
	m.applyEventProjection(host.Event{ID: "model-2", Time: start, FinishedAt: start.Add(time.Second), Failed: true, Category: "MODEL", Summary: "模型响应 错误: 超时", Depth: 1})
	m.applyEventProjection(host.Event{ID: "tool-1", FinishedAt: start.Add(time.Second), Category: "TOOL"})
	if len(m.events) != 2 || m.events[0].ID != "tool-1" || m.events[0].Running() || m.events[1].ID != "model-2" || !m.events[1].Failed {
		t.Fatalf("events = %+v", m.events)
	}
}

// TestRenderStatusBar 守护底部状态栏的信息契约：模型身份（窗口+思考）、会话令牌、
// 花费/预算、书目录都必须在（样式剥离后按纯文本断言）。
func TestRenderStatusBar(t *testing.T) {
	out := ansi.Strip(renderStatusBar(host.UISnapshot{
		Provider:           "openrouter",
		ModelName:          "test-model",
		ModelContextWindow: 200000,
		ThinkingLevel:      "medium",
		TotalInputTokens:   1_234_000,
		TotalOutputTokens:  89_300,
		TotalCostUSD:       0.31,
		BudgetLimitUSD:     5,
		TotalSavedUSD:      0.12,
	}, "/tmp/output", 120))
	for _, want := range []string{"test-model(200K,med)", "↑1.2M", "↓89.3k", "$0.31/$5.00", "省$0.12", "./output"} {
		if !strings.Contains(out, want) {
			t.Fatalf("状态栏缺少 %q：%q", want, out)
		}
	}
}

func TestRenderStatusBarAutoThinkingAndEmpty(t *testing.T) {
	out := ansi.Strip(renderStatusBar(host.UISnapshot{
		ModelName:          "test-model",
		ModelContextWindow: 128000,
	}, "", 120))
	if !strings.Contains(out, "test-model(128K,auto)") {
		t.Fatalf("缺思考等级 auto 括注：%q", out)
	}
	if out := ansi.Strip(renderStatusBar(host.UISnapshot{}, "", 120)); out != "READY" {
		t.Fatalf("空快照应回退 READY，得 %q", out)
	}
}

func TestRenderUsageLineSeparatesFullWidthNameAndTokens(t *testing.T) {
	out := renderUsageLine("gpt-5.6-sol", bodyTextColor, 5300, 0, 0.23, 32)
	if !strings.Contains(out, "gpt-5.6-sol 5.3k") {
		t.Fatalf("model name and tokens should have a visible gap: %q", out)
	}
}

func TestTruncateByDisplayWidth(t *testing.T) {
	// 纯中文按视觉宽度截：10 列预算 = 3 个汉字(6列) + "..."(3列)，按 rune 截会溢出到 17 列
	got := truncate("临港市公共算法伦理审计员", 10)
	if w := lipgloss.Width(got); w > 10 {
		t.Errorf("truncate 溢出列宽: %d > 10 (%q)", w, got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("超宽截断应带省略号: %q", got)
	}
	// ASCII 行为与旧实现一致
	if got := truncate("abcdef", 6); got != "abcdef" {
		t.Errorf("未超宽不应截断: %q", got)
	}
	if got := truncate("abcdefgh", 6); got != "abc..." {
		t.Errorf("ASCII 截断: got %q want %q", got, "abc...")
	}
}

func TestRenderDetailContentWrapsCJK(t *testing.T) {
	long := "沈砚（主角；临港市公共算法伦理审计员，台风夜事故的调查负责人，坚持程序正义）"
	const contentW = 40
	out := renderDetailContent(host.UISnapshot{
		Characters:       []string{long},
		SupportingCount:  1,
		RecentSupporting: []string{long},
		RecentSummaries:  []string{"第6章：" + long},
	}, contentW)
	for line := range strings.SplitSeq(out, "\n") {
		if w := lipgloss.Width(line); w > contentW {
			t.Errorf("行溢出面板宽度: %d > %d (%q)", w, contentW, line)
		}
	}
	// 长描述应折成多行（悬挂缩进续行），而不是截断丢信息
	joined := strings.ReplaceAll(strings.ReplaceAll(out, "\n", ""), " ", "")
	if !strings.Contains(joined, "坚持程序正义") {
		t.Errorf("折行后应保留完整描述，实际输出:\n%s", out)
	}
}
