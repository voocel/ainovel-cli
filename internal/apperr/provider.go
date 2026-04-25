package apperr

import (
	"context"
	"errors"
	"strings"

	"github.com/voocel/litellm"
)

const (
	CodeProviderRequestFailed   Code = "PROVIDER_REQUEST_FAILED"
	CodeProviderAuth            Code = "PROVIDER_AUTH"
	CodeProviderRateLimit       Code = "PROVIDER_RATE_LIMIT"
	CodeProviderTimeout         Code = "PROVIDER_TIMEOUT"
	CodeProviderStreamIdle      Code = "PROVIDER_STREAM_IDLE"
	CodeProviderNetwork         Code = "PROVIDER_NETWORK"
	CodeProviderContextOverflow Code = "PROVIDER_CONTEXT_OVERFLOW"
	CodeProviderResponseInvalid Code = "PROVIDER_RESPONSE_INVALID"
)

// streamIdleMsgPattern matches the rendered message of a stream-idle abort.
// Used as a fallback when the original error chain has been serialized away
// (e.g. inside a sub-agent JSON result).
const streamIdleMsgPattern = "stream idle timeout"

// IsStreamIdleMessage reports whether s contains the rendered marker of a
// stream idle-timeout abort. Useful for paths where only the error string
// survives (sub-agent JSON results, structured event payloads).
func IsStreamIdleMessage(s string) bool {
	return strings.Contains(strings.ToLower(s), streamIdleMsgPattern)
}

// ClassifyProviderError 为运行时 provider 错误补充稳定错误码。
// 如果错误本身已经带 code，则原样返回。
func ClassifyProviderError(err error, op string) error {
	if err == nil {
		return nil
	}
	if CodeOf(err) != CodeUnknown {
		return err
	}

	code := classifyProviderCode(err)
	if code == CodeUnknown {
		return Wrap(err, CodeProviderRequestFailed, op, "provider request failed")
	}
	return Wrap(err, code, op, "provider request failed")
}

func classifyProviderCode(err error) Code {
	if err == nil {
		return CodeUnknown
	}
	// 流式空闲超时优先于通用 timeout：它是连接卡死，failover 一般能救回，
	// 而通用 timeout 可能是模型确实在思考。
	if litellm.IsStreamIdleError(err) {
		return CodeProviderStreamIdle
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeProviderTimeout
	}

	msg := strings.ToLower(err.Error())

	switch {
	case strings.Contains(msg, streamIdleMsgPattern):
		return CodeProviderStreamIdle
	case containsAny(msg, "rate limit", "too many requests", "429"):
		return CodeProviderRateLimit
	case containsAny(msg, "deadline exceeded", "timeout", "timed out"):
		return CodeProviderTimeout
	case containsAny(msg, "invalid api key", "incorrect api key", "unauthorized", "authentication failed", "forbidden", "401", "403"):
		return CodeProviderAuth
	case containsAny(msg, "context length", "maximum context length", "context window", "token limit", "too many tokens", "prompt is too long"):
		return CodeProviderContextOverflow
	case containsAny(msg, "connection refused", "connection reset", "no such host", "dial tcp", "tls handshake timeout", "server misbehaving", "broken pipe", "eof"):
		return CodeProviderNetwork
	case containsAny(msg, "invalid response", "malformed response", "unexpected response", "decode response"):
		return CodeProviderResponseInvalid
	default:
		return CodeUnknown
	}
}

func containsAny(msg string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}
