package apperr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/voocel/litellm"
)

func TestClassifyProviderCodeStreamIdleFromErrorChain(t *testing.T) {
	// Simulates the live error returned by litellm when its watchdog fires.
	llmErr := &litellm.LiteLLMError{
		Type:     litellm.ErrorTypeTimeout,
		Provider: "openai",
		Message:  "stream idle timeout: no chunks received for 1m30s",
		Cause:    litellm.ErrStreamIdle,
	}

	if got := classifyProviderCode(llmErr); got != CodeProviderStreamIdle {
		t.Fatalf("classifyProviderCode = %q, want %q", got, CodeProviderStreamIdle)
	}
}

func TestClassifyProviderCodeStreamIdleFromMessage(t *testing.T) {
	// Simulates an error after the chain has been flattened (e.g. wrapped by
	// agentcore subagent into a plain error string).
	wrapped := fmt.Errorf("Agent %q failed: llm call failed: [litellm:openai:timeout] stream idle timeout: no chunks received for 1m30s", "writer")

	if got := classifyProviderCode(wrapped); got != CodeProviderStreamIdle {
		t.Fatalf("classifyProviderCode = %q, want %q", got, CodeProviderStreamIdle)
	}
}

func TestClassifyProviderCodeIdleBeatsGenericTimeout(t *testing.T) {
	// Idle takes precedence over generic timeout regardless of cause ordering.
	err := &litellm.LiteLLMError{
		Type:    litellm.ErrorTypeTimeout,
		Message: "request timeout while reading stream idle timeout body",
		Cause:   litellm.ErrStreamIdle,
	}
	if got := classifyProviderCode(err); got != CodeProviderStreamIdle {
		t.Fatalf("expected stream_idle to win over timeout, got %q", got)
	}
}

func TestIsStreamIdleMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"plain", "stream idle timeout: no chunks received for 90s", true},
		{"wrapped", "[litellm:openai:timeout] stream idle timeout: ...", true},
		{"case insensitive", "STREAM IDLE TIMEOUT", true},
		{"unrelated timeout", "context deadline exceeded", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStreamIdleMessage(tc.msg); got != tc.want {
				t.Fatalf("IsStreamIdleMessage(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestClassifyProviderErrorAttachesCode(t *testing.T) {
	llmErr := &litellm.LiteLLMError{
		Type:    litellm.ErrorTypeTimeout,
		Message: "stream idle timeout: no chunks received for 90s",
		Cause:   litellm.ErrStreamIdle,
	}
	classified := ClassifyProviderError(llmErr, "test.op")
	if CodeOf(classified) != CodeProviderStreamIdle {
		t.Fatalf("CodeOf = %q, want %q", CodeOf(classified), CodeProviderStreamIdle)
	}
	// Original error still recoverable via the chain.
	if !errors.Is(classified, litellm.ErrStreamIdle) {
		t.Fatal("classified error lost the underlying ErrStreamIdle cause")
	}
}
