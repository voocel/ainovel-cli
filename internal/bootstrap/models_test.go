package bootstrap

import "testing"

func TestNewModelSetUsesOpenRouterProvider(t *testing.T) {
	cfg := Config{
		Provider:  "openrouter",
		ModelName: "stepfun/step-3.5-flash:free",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "test-key", BaseURL: "https://openrouter.ai/api/v1"},
		},
	}
	ms, err := NewModelSet(cfg)
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}

	if provider := ms.Default.ProviderName(); provider != "openrouter" {
		t.Fatalf("expected provider openrouter, got %q", provider)
	}
}
