package llmretry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/progress"
	"github.com/voocel/litellm"
)

type scriptedClient struct {
	results []result
	calls   int
}

type result struct {
	resp *litellm.Response
	err  error
}

func (c *scriptedClient) Chat(context.Context, litellm.Request) (*litellm.Response, error) {
	r := c.results[c.calls]
	c.calls++
	return r.resp, r.err
}

func temporaryErr() error {
	return litellm.NewError("test", litellm.ErrorTypeRateLimit, "slow down", nil)
}

func TestRetryDelayCapsWithoutOverflow(t *testing.T) {
	if got := retryDelay(nil, 10_000); got != maxRetryDelay {
		t.Fatalf("retryDelay = %s, want %s", got, maxRetryDelay)
	}
	if got := retryDelay(nil, 3); got != 8*time.Second {
		t.Fatalf("retryDelay = %s, want 8s", got)
	}
}

func TestRetryDelayHonorsServerHint(t *testing.T) {
	err := litellm.NewError("test", litellm.ErrorTypeRateLimit, "slow down", nil)
	err.RetryAfter = 2 * time.Second
	if got := retryDelay(err, 5); got != 2*time.Second {
		t.Fatalf("retryDelay = %s, want 2s", got)
	}
	err.RetryAfter = 10 * time.Minute
	if got := retryDelay(err, 0); got != maxRetryDelay {
		t.Fatalf("retryDelay 应封顶 = %s, want %s", got, maxRetryDelay)
	}
}

func TestIsRetryableOnlyTemporary(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"网络抖动":         {litellm.NewError("test", litellm.ErrorTypeNetwork, "reset", nil), true},
		"限流":           {temporaryErr(), true},
		"过载":           {litellm.NewError("test", litellm.ErrorTypeOverloaded, "busy", nil), true},
		"参数校验":         {litellm.NewError("test", litellm.ErrorTypeValidation, "bad request", nil), false},
		"鉴权":           {litellm.NewError("test", litellm.ErrorTypeAuth, "denied", nil), false},
		"上下文溢出":        {litellm.NewError("test", litellm.ErrorTypeContextOverflow, "too long", nil), false},
		"超时":           {litellm.NewError("test", litellm.ErrorTypeTimeout, "slow", nil), false},
		"取消":           {context.Canceled, false},
		"非 litellm 错误": {errors.New("boom"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Fatalf("isRetryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChatRetriesTemporaryAndReports(t *testing.T) {
	want := &litellm.Response{Blocks: []litellm.Block{litellm.Text("{}")}}
	client := &scriptedClient{results: []result{
		{err: temporaryErr()},
		{resp: want},
	}}
	var events []Event
	got, err := Chat(t.Context(), client, Config{
		Agent:   "test",
		OnRetry: func(ev Event) { events = append(events, ev) },
	}, litellm.Request{Model: "m"})
	if err != nil || got != want {
		t.Fatalf("Chat = %v %v", got, err)
	}
	if client.calls != 2 || len(events) != 1 || events[0].Attempt != 1 || events[0].Delay != time.Second {
		t.Fatalf("calls=%d events=%+v", client.calls, events)
	}
}

func TestChatReturnsTerminalErrorImmediately(t *testing.T) {
	terminal := litellm.NewError("test", litellm.ErrorTypeValidation, "bad request", nil)
	client := &scriptedClient{results: []result{{err: terminal}}}
	_, err := Chat(t.Context(), client, Config{}, litellm.Request{Model: "m"})
	if !errors.Is(err, terminal) {
		t.Fatalf("应原样返回终止错误, got %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("终止错误不应重试, calls=%d", client.calls)
	}
}

func TestChatReportsRetryProgress(t *testing.T) {
	client := &scriptedClient{results: []result{
		{err: temporaryErr()},
		{resp: &litellm.Response{}},
	}}
	var reports []progress.Event
	ctx := agentcore.WithProgress(t.Context(), func(p any) {
		if ev, ok := progress.From(p); ok {
			reports = append(reports, ev)
		}
	})
	if _, err := Chat(ctx, client, Config{Agent: "arbiter"}, litellm.Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("应上报一次重试进度: %+v", reports)
	}
	got := reports[0]
	if got.Kind != progress.KindRetry || got.Agent != "arbiter" || got.Attempt != 1 || got.Message == "" {
		t.Fatalf("重试进度载荷不符: %+v", got)
	}
	var meta struct {
		DelayMS int64 `json:"retry_delay_ms"`
	}
	if err := json.Unmarshal(got.Meta, &meta); err != nil || meta.DelayMS != 1000 {
		t.Fatalf("退避快照不符: %s %v", got.Meta, err)
	}
}

func TestChatStopsOnCanceledContext(t *testing.T) {
	client := &scriptedClient{results: []result{{err: temporaryErr()}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Chat(ctx, client, Config{}, litellm.Request{Model: "m"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context 错误, got %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("取消后不应重试, calls=%d", client.calls)
	}
}
