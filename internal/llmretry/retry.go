// Package llmretry 是直接模型调用共用的请求层重试内核：仅重试 provider 标记为
// 临时的错误（网络、限流、过载），遵守 Retry-After/指数退避，并经 OnRetry 与
// 工具进度把重试送入既有工作台观察链。鉴权、参数校验、上下文溢出、取消等终止
// 错误立即返回；临时错误持续重试，生命周期只由 context 控制。
package llmretry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/voocel/ainovel-cli/internal/progress"
	"github.com/voocel/litellm"
)

const maxRetryDelay = 60 * time.Second

// Client 是请求重试所需的最小模型接口；*litellm.Client 满足它。
type Client interface {
	Chat(context.Context, litellm.Request) (*litellm.Response, error)
}

// Event 描述一次即将发生的请求重试。
type Event struct {
	Attempt int
	Delay   time.Duration
	Err     error
}

// Config 配置重试的可观测信息，不改变重试语义。
type Config struct {
	Agent   string
	OnRetry func(Event)
}

// Chat 调用 client.Chat。临时错误退避后持续重试，直到成功或 context 结束；
// 非临时错误立即返回。
func Chat(ctx context.Context, client Client, cfg Config, req litellm.Request) (*litellm.Response, error) {
	for retry := 1; ; retry++ {
		resp, err := client.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isRetryable(err) {
			return nil, err
		}

		delay := retryDelay(err, retry-1)
		if cfg.OnRetry != nil {
			cfg.OnRetry(Event{Attempt: retry, Delay: delay, Err: err})
		}
		progress.Report(ctx, progress.Event{
			Kind:    progress.KindRetry,
			Agent:   cfg.Agent,
			Attempt: retry,
			Message: err.Error(),
			Meta:    retryMeta(delay),
		})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// retryMeta 带上本次退避的快照，供观察链逐秒倒计时而不必本地估算。
func retryMeta(delay time.Duration) json.RawMessage {
	meta, _ := json.Marshal(struct {
		DelayMS int64 `json:"retry_delay_ms"`
	}{DelayMS: delay.Milliseconds()})
	return meta
}

// isRetryable 只放行 provider 声明的临时错误；本地校验、取消与超时都不是可
// 重试的边界。
func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch litellm.ErrorTypeOf(err) {
	case litellm.ErrorTypeCanceled, litellm.ErrorTypeTimeout, litellm.ErrorTypeValidation:
		return false
	}
	return litellm.IsTemporaryError(err)
}

func retryDelay(err error, attempt int) time.Duration {
	if delay := litellm.RetryAfter(err); delay > 0 {
		if delay > maxRetryDelay {
			return maxRetryDelay
		}
		return delay
	}
	delay := time.Second
	for i := 0; i < attempt && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}
