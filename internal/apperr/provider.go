package apperr

import (
	"context"
	"errors"
	"strings"
)

const (
	CodeProviderRequestFailed   Code = "PROVIDER_REQUEST_FAILED"
	CodeProviderAuth            Code = "PROVIDER_AUTH"
	CodeProviderRateLimit       Code = "PROVIDER_RATE_LIMIT"
	CodeProviderTimeout         Code = "PROVIDER_TIMEOUT"
	CodeProviderNetwork         Code = "PROVIDER_NETWORK"
	CodeProviderContextOverflow Code = "PROVIDER_CONTEXT_OVERFLOW"
	CodeProviderResponseInvalid Code = "PROVIDER_RESPONSE_INVALID"
)

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
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeProviderTimeout
	}

	msg := strings.ToLower(err.Error())

	switch {
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
