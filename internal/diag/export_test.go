package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// sentinel 是一段绝不该出现在Xuất里的"小说Chính văn"。
const sentinel = "雪夜里主角揭穿了反派的惊天阴谋这是机密Chính văn"

// writeSession 把若干消息按 sessions/*.jsonl 的格式写到临时 output Thư mục。
func writeSession(t *testing.T, rel string, msgs []agentcore.Message) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "meta", "sessions", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var b strings.Builder
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func commitCall(chapterRaw string) agentcore.Message {
	args := json.RawMessage(`{"chapter":` + chapterRaw + `,"content":"` + sentinel + sentinel + `"}`)
	return agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{Name: "commit_chapter", Args: args})},
	}
}

func errResult(msg string) agentcore.Message {
	return agentcore.Message{
		Role:     agentcore.RoleTool,
		Content:  []agentcore.ContentBlock{agentcore.TextBlock(msg)},
		Metadata: map[string]any{"is_error": true},
	}
}

// TestExport_DeathLoopShape 端到端复现 #34：Mô hình把 commit_chapter 的 chapter
// 字符串化导致校验循环。断言Xuất能定位、且小说Chính văn零出包。
func TestExport_DeathLoopShape(t *testing.T) {
	var msgs []agentcore.Message
	// 一段裸的 coordinator 规划Chính văn（<4KB，绕过 session_compact），必须被打码。
	msgs = append(msgs, agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(sentinel)},
	})
	// 14 轮 commit_chapter(chapter:"7") + InputValidationError。
	for range 14 {
		msgs = append(msgs, commitCall(`"7"`))
		msgs = append(msgs, errResult("InputValidationError: chapter must be int"))
	}

	dir := writeSession(t, "coordinator.jsonl", msgs)
	s := store.NewStore(dir)
	rep, rc := Diagnose(s)
	out := string(RenderExport(rep, rc))

	if strings.Contains(out, sentinel) {
		t.Fatalf("小说Chính văn出包了！Xuất包含 sentinel:\n%s", out)
	}
	if !strings.Contains(out, `chapter: "7"`) {
		t.Errorf("缺类型异常信号 chapter: \"7\"（#34 根因）\n%s", out)
	}
	if !strings.Contains(out, "InputValidationError") {
		t.Errorf("Lỗi串未保留\n%s", out)
	}
	if !strings.Contains(out, "×14") {
		t.Errorf("重复聚合未列出 ×14\n%s", out)
	}
	// Phase 2：运行时检测应把这个循环判成 critical 的 RepeatedToolError。
	if !strings.Contains(out, "工具反复报同一Lỗi") {
		t.Errorf("运行时检测未产出 RepeatedToolError\n%s", out)
	}
	if !strings.Contains(out, "[critical]") {
		t.Errorf("14 次重复应升为 critical\n%s", out)
	}
}

// TestExport_NumberVsStringArg 证明标量与字符串投影能区分类型：
// chapter:7（数字）保留为 7，chapter:"7"（字符串）保留为 "7"。
func TestExport_NumberVsStringArg(t *testing.T) {
	intDir := writeSession(t, "coordinator.jsonl", []agentcore.Message{commitCall(`7`)})
	si := store.NewStore(intDir)
	repInt, rcInt := Diagnose(si)
	outInt := string(RenderExport(repInt, rcInt))
	if !strings.Contains(outInt, "chapter: 7") || strings.Contains(outInt, `chapter: "7"`) {
		t.Errorf("数字参数应渲染为 chapter: 7（不带引号）\n%s", outInt)
	}
}

// TestProjectValue_ProseArgRedacted 守护脱敏边界：标识符型短值保留、
// 中文/带Rỗng格的短值（如 dispatch task、chapter title）一律打码。
func TestProjectValue_ProseArgRedacted(t *testing.T) {
	keep := map[string]string{
		`"7"`:       `"7"`,       // 字符串化数字（#34 信号）
		`"premise"`: `"premise"`, // 枚举
		`"writer"`:  `"writer"`,  // 角色名
		`7`:         `7`,         // 数字标量
		`true`:      `true`,      // bool 标量
	}
	for in, want := range keep {
		if got := projectValue([]byte(in)); got != want {
			t.Errorf("应保留 %s：got %q want %q", in, got, want)
		}
	}
	// 含中文 / Rỗng格 → 必须打码，且不含原文。
	prose := []string{`"第7章 雪夜的真相"`, `"雪夜杀机"`, `"主角揭穿阴谋"`}
	for _, in := range prose {
		got := projectValue([]byte(in))
		if !strings.HasPrefix(got, "<redacted") {
			t.Errorf("中文/带Rỗng格短值应打码：%s → %q", in, got)
		}
		if strings.Contains(got, "雪夜") || strings.Contains(got, "主角") {
			t.Errorf("打码后仍含Chính văn：%s → %q", in, got)
		}
	}
}

// TestWriteExport_WritesFile 证明纯函数Đường dẫn：不依赖 TUI，写出固定相对Đường dẫn。
func TestWriteExport_WritesFile(t *testing.T) {
	dir := writeSession(t, "coordinator.jsonl", []agentcore.Message{commitCall(`"7"`), errResult("boom")})
	s := store.NewStore(dir)

	rep, rc := Diagnose(s)
	path, err := WriteExport(s, rep, rc)
	if err != nil {
		t.Fatalf("WriteExport: %v", err)
	}
	if want := filepath.Join(dir, filepath.FromSlash(ExportRelPath)); path != want {
		t.Errorf("Đường dẫn不对：got %s want %s", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "diag-export") {
		t.Errorf("Tập tin内容异常\n%s", data)
	}
	if strings.Contains(string(data), sentinel) {
		t.Errorf("写出的Tập tin夹带Chính văn")
	}
}

// TestRedactMessage_DupSha 证明同一段文本反复出现产生同 sha（循环信号）。
func TestRedactMessage_DupSha(t *testing.T) {
	a := redactMessage("coordinator", agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(sentinel)},
	})
	b := redactMessage("coordinator", agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(sentinel)},
	})
	if a.TextSha == "" || a.TextSha != b.TextSha {
		t.Errorf("相同Chính văn应得相同 sha：%q vs %q", a.TextSha, b.TextSha)
	}
	if a.Redacted != 1 {
		t.Errorf("应打码 1 个文本块，got %d", a.Redacted)
	}
}
