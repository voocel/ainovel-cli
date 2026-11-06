package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// TestSessionStore_MetaInjected_AssistantWithUsage 验证只有"assistant + has Usage"
// 的消息才被附加 _meta，这是 replay 路径精确算价的前提。
// 同时锁定 JSONL 落盘形态：agentcore v1.8.8 的 message 用 blocks（不是 content）、
// stop（不是 stop_reason），消息级 provider/model 在顶层。
func TestSessionStore_MetaInjected_AssistantWithUsage(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(newIO(dir))
	lookup := ModelLookup(func(agentName string) (string, string) {
		return "meme", "gpt-5.4"
	})
	logger := s.SubAgentLogger(lookup)

	logger("writer", "写第 1 章", agentcore.UserText("写第 1 章"))
	logger("writer", "写第 1 章", agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{litellm.Text("第 1 章已开写")},
		Stop:   agentcore.StopEnd,
		Usage: &agentcore.Usage{Usage: litellm.Usage{
			InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 800,
		}},
	})
	logger("writer", "写第 1 章", agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{litellm.Text("流式没等到 final usage chunk")},
		Stop:   agentcore.StopEnd,
		// Usage nil：流式未带 final usage chunk
	})

	entries := readJSONL(t, filepath.Join(dir, "meta/sessions/agents/writer-ch01.jsonl"))
	if len(entries) != 3 {
		t.Fatalf("entries=%d want 3", len(entries))
	}
	if _, has := entries[0]["_meta"]; has {
		t.Errorf("user message should NOT have _meta")
	}
	if _, has := entries[2]["_meta"]; has {
		t.Errorf("assistant without Usage should NOT have _meta")
	}
	meta, ok := entries[1]["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("assistant+Usage should have _meta map, got %T %v", entries[1]["_meta"], entries[1]["_meta"])
	}
	if meta["provider"] != "meme" || meta["model"] != "gpt-5.4" {
		t.Errorf("_meta = %v want provider=meme model=gpt-5.4", meta)
	}
	if _, has := entries[0]["usage"]; has {
		t.Errorf("user message should NOT have usage")
	}
	assertTextBlocks(t, entries[0], "写第 1 章")
	assertTextBlocks(t, entries[1], "第 1 章已开写")
	if got := entries[1]["stop"]; got != string(agentcore.StopEnd) {
		t.Errorf("stop = %v want %q", got, agentcore.StopEnd)
	}
	if _, has := entries[1]["stop_reason"]; has {
		t.Errorf("legacy stop_reason field should be gone")
	}
	usage, ok := entries[1]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage should be map, got %T %v", entries[1]["usage"], entries[1]["usage"])
	}
	if usage["input_tokens"] != float64(1000) || usage["output_tokens"] != float64(200) || usage["cache_read_tokens"] != float64(800) {
		t.Errorf("usage = %v want input_tokens=1000 output_tokens=200 cache_read_tokens=800", usage)
	}
}

// TestSessionStore_MetaPrefersMessageProvider 验证消息自带 provider/model 时优先用它，
// 而不是 logger 传入的 lookup：v1.8.8 把这两个字段放到了 agentcore.Message 顶层，
// 落盘后既是顶层字段也是 _meta 来源。
func TestSessionStore_MetaPrefersMessageProvider(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(newIO(dir))
	logger := s.SubAgentLogger(ModelLookup(func(agentName string) (string, string) {
		return "lookup-prov", "lookup-model"
	}))

	msg := makeAssistantWithUsage()
	msg.Provider = "abliteration"
	msg.Model = "abliterated-model-large-v2"
	logger("writer", "写第 1 章", msg)

	entries := readJSONL(t, filepath.Join(dir, "meta/sessions/agents/writer-ch01.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("entries=%d want 1", len(entries))
	}
	if got := entries[0]["provider"]; got != "abliteration" {
		t.Errorf("provider = %v want abliteration", got)
	}
	if got := entries[0]["model"]; got != "abliterated-model-large-v2" {
		t.Errorf("model = %v want abliterated-model-large-v2", got)
	}
	meta, ok := entries[0]["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("entry missing _meta")
	}
	if meta["provider"] != "abliteration" || meta["model"] != "abliterated-model-large-v2" {
		t.Errorf("_meta = %v want message provider/model", meta)
	}
}

// TestSessionStore_MetaModelSwitch 验证运行中切换模型后，后续消息的 _meta 也跟着变。
// 这是 B 方案对"同进程内 /model 切换"的精确支持。
func TestSessionStore_MetaModelSwitch(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(newIO(dir))

	current := "model-a"
	lookup := ModelLookup(func(agentName string) (string, string) {
		return "meme", current
	})
	logger := s.SubAgentLogger(lookup)

	logger("writer", "写第 1 章", makeAssistantWithUsage())
	current = "model-b" // 模拟 /model 切换
	logger("writer", "写第 1 章", makeAssistantWithUsage())

	entries := readJSONL(t, filepath.Join(dir, "meta/sessions/agents/writer-ch01.jsonl"))
	if len(entries) != 2 {
		t.Fatalf("entries=%d want 2", len(entries))
	}
	for i, want := range []string{"model-a", "model-b"} {
		meta, ok := entries[i]["_meta"].(map[string]any)
		if !ok {
			t.Fatalf("entry[%d] missing _meta", i)
		}
		if got := meta["model"]; got != want {
			t.Errorf("entry[%d] model = %v want %s", i, got, want)
		}
	}
}

// TestSessionStore_NilLookup 验证 lookup=nil 时写入仍然正常，
// 只是不带 _meta。
func TestSessionStore_NilLookup(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(newIO(dir))
	logger := s.SubAgentLogger(nil)
	logger("writer", "写第 1 章", makeAssistantWithUsage())

	rel, err := s.subAgentPath("writer", "写第 1 章")
	if err != nil {
		t.Fatal(err)
	}
	entries := readJSONL(t, filepath.Join(dir, rel))
	if len(entries) != 1 {
		t.Fatalf("entries=%d want 1", len(entries))
	}
	if _, has := entries[0]["_meta"]; has {
		t.Errorf("nil lookup should not produce _meta")
	}
	// 但其他字段（role/usage）必须正常
	if entries[0]["role"] != "assistant" {
		t.Errorf("role lost: %v", entries[0]["role"])
	}
	if _, has := entries[0]["usage"]; !has {
		t.Errorf("usage lost: %v", entries[0])
	}
}

func TestSessionStoreContinuesAgentSequenceAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	first := NewSessionStore(newIO(dir)).SubAgentLogger(nil)
	first("architect_long", "处理反馈", makeAssistantWithUsage())

	second := NewSessionStore(newIO(dir)).SubAgentLogger(nil)
	second("architect_long", "扩展大纲", makeAssistantWithUsage())

	if got := len(readJSONL(t, filepath.Join(dir, "meta/sessions/agents/architect_long-001.jsonl"))); got != 1 {
		t.Fatalf("first session entries = %d, want 1", got)
	}
	if got := len(readJSONL(t, filepath.Join(dir, "meta/sessions/agents/architect_long-002.jsonl"))); got != 1 {
		t.Fatalf("second session entries = %d, want 1", got)
	}
}

// TestSessionStore_CompactToolResultUsesToolUseName 验证 v1.8.8 下主动压缩仍能拿到工具名：
// tool result 块只带 tool_use_id，工具名来自此前 assistant 消息里的 tool_use 块。
func TestSessionStore_CompactToolResultUsesToolUseName(t *testing.T) {
	dir := t.TempDir()
	s := NewSessionStore(newIO(dir))
	logger := s.SubAgentLogger(nil)

	logger("writer", "写第 1 章", agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{litellm.ToolUseBlock{ID: "call-1", Name: "novel_context"}},
		Stop:   agentcore.StopToolUse,
	})
	big := `{"_loading_summary":"近三章概要","body":"` + strings.Repeat("x", 5000) + `"}`
	logger("writer", "写第 1 章", agentcore.ToolResult("call-1", agentcore.Result{Content: []litellm.Block{litellm.Text(big)}}))

	entries := readJSONL(t, filepath.Join(dir, "meta/sessions/agents/writer-ch01.jsonl"))
	if len(entries) != 2 {
		t.Fatalf("entries=%d want 2", len(entries))
	}
	if got := entries[0]["stop"]; got != string(agentcore.StopToolUse) {
		t.Errorf("assistant stop = %v want %q", got, agentcore.StopToolUse)
	}
	blocks, ok := entries[1]["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("tool result blocks = %v want one block", entries[1]["blocks"])
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %T", blocks[0])
	}
	if block["type"] != "tool_result" || block["tool_use_id"] != "call-1" {
		t.Fatalf("block = %v want type=tool_result tool_use_id=call-1", block)
	}
	content, ok := block["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("tool result content = %v want one text block", block["content"])
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[session_compact: novel_context ") {
		t.Errorf("tool result text = %.120q want novel_context placeholder", text)
	}
	if !strings.Contains(text, "近三章概要") {
		t.Errorf("tool result text lost _loading_summary: %.160q", text)
	}
}

func makeAssistantWithUsage() agentcore.Message {
	return agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{litellm.Text("继续")},
		Stop:   agentcore.StopEnd,
		Usage: &agentcore.Usage{Usage: litellm.Usage{
			InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 800,
		}},
	}
}

// assertTextBlocks 断言条目按 v1.8.8 形态落盘：blocks 是 [{type:text,text:...}]，
// 且不再有旧的 content 字段。
func assertTextBlocks(t *testing.T, entry map[string]any, want string) {
	t.Helper()
	if _, has := entry["content"]; has {
		t.Errorf("legacy content field should be gone: %v", entry)
	}
	blocks, ok := entry["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("blocks = %v want one text block", entry["blocks"])
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %T %v", blocks[0], blocks[0])
	}
	if block["type"] != "text" || block["text"] != want {
		t.Errorf("block = %v want type=text text=%q", block, want)
	}
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("unmarshal line: %v\n%s", err, string(line))
		}
		out = append(out, m)
	}
	return out
}
