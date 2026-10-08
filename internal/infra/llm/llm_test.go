package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/infra/llm"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// routedProvider 声明接受缓存与会话路由选项，像 OpenAI 那样。
type routedProvider struct{ *litellmtest.Provider }

func (routedProvider) Capabilities() litellm.Capabilities {
	return litellm.Capabilities{ProviderOptions: []string{"prompt_cache_key", "session_id"}}
}

func binding(t *testing.T, p litellm.Provider) models.Binding {
	t.Helper()
	client, err := litellm.New(p)
	if err != nil {
		t.Fatal(err)
	}
	return models.Binding{Chat: agentcore.Model{Client: client, Request: litellm.Request{Model: "m", Thinking: &litellm.Thinking{Effort: "high"}}}}
}

var call = llm.Call{
	System:    "判定器",
	Input:     `{"x":1}`,
	Schema:    llm.Schema{Name: "verdict", Description: "测试", JSON: map[string]any{"type": "object"}},
	CacheKey:  "cache-1",
	SessionID: "session-1",
}

func TestStructuredRequestsSchemaAndDecodesStrictly(t *testing.T) {
	reply := litellmtest.Text(`{"status":"pass"}`)
	reply.Usage = litellm.Usage{InputTokens: 10, OutputTokens: 3}
	p := routedProvider{litellmtest.New(reply, litellmtest.Text(`{"status":"pass","extra":1}`))}
	b := binding(t, p)
	var out struct {
		Status string `json:"status"`
	}
	usage, err := llm.Structured(context.Background(), b, call, &out)
	if err != nil || out.Status != "pass" || usage.InputTokens != 10 {
		t.Fatalf("structured call: err=%v out=%+v usage=%+v", err, out, usage)
	}
	req := p.Requests()[0]
	if f := req.ResponseFormat; f == nil || f.Type != litellm.ResponseFormatJSONSchema || f.JSONSchema.Strict == nil || !*f.JSONSchema.Strict {
		t.Fatalf("strict JSON schema not requested: %+v", f)
	}
	var key, session string
	_ = json.Unmarshal(req.ProviderOptions["prompt_cache_key"], &key)
	_ = json.Unmarshal(req.ProviderOptions["session_id"], &session)
	if key != "cache-1" || session != "session-1" || req.Messages[0].Role != litellm.RoleSystem || req.Thinking == nil || req.Thinking.Effort != "high" {
		t.Fatalf("call identity not forwarded: options=%s messages=%+v thinking=%+v", req.ProviderOptions, req.Messages, req.Thinking)
	}
	if b.Chat.Request.ProviderOptions != nil {
		t.Fatal("routing must not leak into the binding")
	}
	if _, err := llm.Structured(context.Background(), b, call, &out); err == nil {
		t.Fatal("unknown field accepted")
	}
}

// 不列路由选项的厂商拒绝不认识的选项：一个也不能带过去。
func TestStructuredOmitsOptionsTheProviderDoesNotList(t *testing.T) {
	p := litellmtest.New(litellmtest.Text(`{}`))
	var out struct{}
	if _, err := llm.Structured(context.Background(), binding(t, p), call, &out); err != nil {
		t.Fatal(err)
	}
	if options := p.Requests()[0].ProviderOptions; len(options) != 0 {
		t.Fatalf("options = %s", options)
	}
}

// 流式调用按到达顺序把事件交给观察者：思考先于输出，最后是用量与结束。
func TestStructuredStreamsEventsToObserver(t *testing.T) {
	reply := litellmtest.Respond(litellm.ReasoningBlock{Text: "先看约束"}, litellm.Text(`{"status":"pass"}`))
	reply.Usage = litellm.Usage{InputTokens: 7, OutputTokens: 2}
	var kinds []string
	observed := call
	observed.Observe = func(event litellm.Event) error {
		switch e := event.(type) {
		case litellm.ReasoningDelta:
			kinds = append(kinds, "reasoning:"+e.Text)
		case litellm.TextDelta:
			kinds = append(kinds, "text")
		case litellm.UsageEvent:
			kinds = append(kinds, "usage")
		case litellm.DoneEvent:
			kinds = append(kinds, "done")
		}
		return nil
	}
	var out struct {
		Status string `json:"status"`
	}
	usage, err := llm.Structured(context.Background(), binding(t, litellmtest.New(reply)), observed, &out)
	if err != nil || out.Status != "pass" || usage.InputTokens != 7 {
		t.Fatalf("structured call: err=%v out=%+v usage=%+v", err, out, usage)
	}
	if got := strings.Join(kinds, ","); got != "reasoning:先看约束,text,usage,done" {
		t.Fatalf("observed events = %s", got)
	}
}
