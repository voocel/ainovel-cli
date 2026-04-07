package apperr

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyProviderErrorTimeout(t *testing.T) {
	err := ClassifyProviderError(context.DeadlineExceeded, "provider.chat")
	if got := CodeOf(err); got != CodeProviderTimeout {
		t.Fatalf("CodeOf() = %s, want %s", got, CodeProviderTimeout)
	}
}

func TestClassifyProviderErrorRateLimit(t *testing.T) {
	err := ClassifyProviderError(errors.New("429 too many requests"), "provider.chat")
	if got := CodeOf(err); got != CodeProviderRateLimit {
		t.Fatalf("CodeOf() = %s, want %s", got, CodeProviderRateLimit)
	}
}

func TestClassifyProviderErrorKeepsExistingCode(t *testing.T) {
	base := New(CodeProviderAuth, "provider.chat", "认证失败")
	err := ClassifyProviderError(base, "provider.chat")
	if got := CodeOf(err); got != CodeProviderAuth {
		t.Fatalf("CodeOf() = %s, want %s", got, CodeProviderAuth)
	}
}
