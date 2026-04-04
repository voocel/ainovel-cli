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

func TestProviderTypeRejectsUnknownProviderWithoutExplicitType(t *testing.T) {
	pc := ProviderConfig{APIKey: "test-key"}
	typ, err := pc.ProviderType("custom-unknown")
	if err == nil {
		t.Fatal("expected unknown provider type error")
	}
	if typ != "" {
		t.Fatalf("expected empty provider type, got %q", typ)
	}
}
