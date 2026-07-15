package host

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func newModelConfigTestHost(t *testing.T) (*Host, string) {
	t.Helper()
	pc := bootstrap.ProviderConfig{
		Type: "openai", APIKey: "old-secret", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{{Name: "old", ContextWindow: 128000}, {Name: "writer-model"}},
	}
	cfg := bootstrap.Config{
		Provider: "proxy", ModelName: "old", Providers: map[string]bootstrap.ProviderConfig{"proxy": pc},
		Roles: map[string]bootstrap.RoleConfig{"writer": {Provider: "proxy", Model: "writer-model"}},
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("new model set: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	return &Host{
		cfg: cfg, models: models, events: make(chan Event, 4),
		configTargets: []bootstrap.ConfigTarget{{ID: "test", Label: "test", Path: path}},
	}, path
}

func TestConfigureModelsRejectsDeletingReferencedModel(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{{Name: "new"}}, DefaultModel: "new",
		APIKeyAction: APIKeyKeep, TargetID: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("expected writer reference error, got %v", err)
	}
	provider, model, _ := h.models.CurrentSelection("default")
	if provider != "proxy" || model != "old" {
		t.Fatalf("runtime mutated after failure: %s/%s", provider, model)
	}
}

func TestConfigureModelsPersistsAndHotApplies(t *testing.T) {
	h, path := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", API: "responses", BaseURL: "https://new.example/v1",
		Models:       []bootstrap.ModelConfig{{Name: "new", ContextWindow: 640000}, {Name: "writer-model"}},
		DefaultModel: "new", APIKeyAction: APIKeyKeep, TargetID: "test",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	provider, model, _ := h.models.CurrentSelection("default")
	if provider != "proxy" || model != "new" {
		t.Fatalf("runtime selection = %s/%s", provider, model)
	}
	if window, source := h.models.ResolveContextWindow(provider, model); window != 640000 || source != bootstrap.CtxWindowModelConfig {
		t.Fatalf("runtime window = %d %s", window, source)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load saved: %v", err)
	}
	if saved.Provider != "proxy" || saved.ModelName != "new" || saved.Providers["proxy"].APIKey != "old-secret" {
		t.Fatalf("saved config = %#v", saved)
	}
	if len(saved.Providers["proxy"].Models) != 2 || saved.Providers["proxy"].Models[0].ContextWindow != 640000 {
		t.Fatalf("saved models = %#v", saved.Providers["proxy"].Models)
	}
}
