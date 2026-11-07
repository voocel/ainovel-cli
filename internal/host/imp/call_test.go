package imp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// TestCallStructuredNotifiesRetries 守护重试可见性：请求退避与校验重问都必须回显，
// 否则指数退避可静默数分钟，用户会误以为导入卡死（截图问题：3 分钟无声后才报错）。
// 请求退避还必须携带非零 retryAt 截止时刻——UI 倒计时依赖它；校验重问即时发生，retryAt 为零。
func TestCallStructuredNotifiesRetries(t *testing.T) {
	m := &mockProvider{responses: []string{"不是 JSON", `{"boundaries":[]}`}, failFirst: 2}
	var notes []string
	var retries, reasks int
	prof := callProfile{notify: func(s string, retryAt time.Time) {
		notes = append(notes, s)
		if !retryAt.IsZero() {
			retries++
		}
		if strings.Contains(s, "重问") {
			reasks++
		}
	}}
	if _, err := callStructured[boundaryBatch](context.Background(), scriptModel(t, m), segmentContract, "sys", "p", prof, nil); err != nil {
		t.Fatalf("最终应成功：%v", err)
	}
	if retries != 2 || reasks != 1 {
		t.Fatalf("应回显 2 次带截止时刻的请求退避 + 1 次校验重问，得 %d/%d：%v", retries, reasks, notes)
	}
}

// TestBriefErrIncludesAdapterFacts 守护错误回显的可诊断性：网关 message 可能只有一句
// "Provider returned error"，回显必须补上 litellm 携带的结构化事实（分类/HTTP 状态/provider），
// 且事实在前——截断时优先保住它们；非适配器错误保持原样。
func TestBriefErrIncludesAdapterFacts(t *testing.T) {
	le := &litellm.Error{
		Type: litellm.ErrorTypeProvider, StatusCode: 502,
		Provider: "openai", Message: "Provider returned error",
	}
	got := briefErr(fmt.Errorf("外层包装：%w", le))
	for _, want := range []string{"上游服务错误", "HTTP 502", "openai", "Provider returned error"} {
		if !strings.Contains(got, want) {
			t.Fatalf("回显应包含 %q，得 %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "上游服务错误") {
		t.Fatalf("结构化事实应在前，得 %q", got)
	}
	if got := briefErr(errors.New("普通错误")); got != "普通错误" {
		t.Fatalf("非适配器错误应保持原样，得 %q", got)
	}
}

// TestCallStructuredCancelIsNotSemanticFailure 守护取消语义：用户取消（Esc）不是语义失败，
// 不得包装成「N 次尝试」的 errSemantic——那会误导排查方向并多落一份误导性 failures/ 工件。
func TestCallStructuredCancelIsNotSemanticFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &mockProvider{responses: []string{"垃圾输出"}}
	_, err := callStructured[boundaryBatch](ctx, scriptModel(t, m), segmentContract, "sys", "p", callProfile{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，得 %v", err)
	}
	var se *errSemantic
	if errors.As(err, &se) {
		t.Fatal("取消不应被包装成语义失败")
	}
}

// TestCallStructuredCarriesRawOnSemanticFailure 守护 §14.2：输出层契约违约时，
// 错误必须携带原始响应，供 runner 统一落 failures/ 失败工件。
func TestCallStructuredCarriesRawOnSemanticFailure(t *testing.T) {
	m := &mockProvider{responses: []string{"垃圾输出 not json"}}
	_, err := callStructured[boundaryBatch](context.Background(), nativeScriptModel(t, m), segmentContract, "sys", "payload", callProfile{}, nil)
	var se *errSemantic
	if !errors.As(err, &se) {
		t.Fatalf("应返回 errSemantic，得 %T：%v", err, err)
	}
	if se.Raw != "垃圾输出 not json" || !strings.Contains(se.Error(), "契约违约") {
		t.Fatalf("Raw 应携带最后一次原始响应，得 %q", se.Raw)
	}
}

// TestCallStructuredSendsNoOutputLimit 守护输出预算策略：客户端不设输出上限——
// 生成/推理预算由 provider 与模型决定，任何 max_* 键都会把网关的注入上限变成应用缺陷。
func TestCallStructuredSendsNoOutputLimit(t *testing.T) {
	provider := litellmtest.New(litellmtest.Text(`{"boundaries":[]}`))
	model := modelHandle(t, provider, new(false))
	if _, err := callStructured[boundaryBatch](context.Background(), model, segmentContract, "sys", "p", callProfile{thinking: llmcontract.ThinkingHigh}, nil); err != nil {
		t.Fatalf("callStructured: %v", err)
	}
	req := provider.Requests()[0]
	if req.MaxTokens != nil {
		t.Fatalf("不应设置输出上限, got max_tokens=%d", *req.MaxTokens)
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := req.ProviderOptions[key]; ok {
			t.Fatalf("不应设置输出上限, 请求携带 %s", key)
		}
	}
}

func TestCallStructuredCarriesRawOnProtocolFailure(t *testing.T) {
	m := &mockProvider{
		responses: []string{"upstream malformed output"},
		stop:      litellm.FinishReasonError,
	}
	_, err := callStructured[boundaryBatch](context.Background(), nativeScriptModel(t, m), segmentContract, "sys", "payload", callProfile{}, nil)
	var se *errSemantic
	if !errors.As(err, &se) || se.Raw != "upstream malformed output" || !strings.Contains(se.Error(), "stop_reason=error") {
		t.Fatalf("协议错误应携带原始响应，得 %T：%v", err, err)
	}
}
