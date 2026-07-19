package llmretry

import (
	"testing"
	"time"
)

func TestRetryDelayCapsWithoutOverflow(t *testing.T) {
	if got := retryDelay(nil, 10_000); got != maxRetryDelay {
		t.Fatalf("retryDelay = %s, want %s", got, maxRetryDelay)
	}
	if got := retryDelay(nil, 3); got != 8*time.Second {
		t.Fatalf("retryDelay = %s, want 8s", got)
	}
}
