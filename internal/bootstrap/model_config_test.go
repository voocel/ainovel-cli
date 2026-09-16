package bootstrap

import (
	"testing"

	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
)

func TestExecutionModelResolvesConnectionProtocol(t *testing.T) {
	c := appconfig.Config{Provider: "公司代理", Model: "custom-model", Providers: map[string]appconfig.ProviderConfig{
		"公司代理":  {Type: "openai", API: "responses", APIKey: "correct", BaseURL: "https://example.com/v1"},
		"other": {Type: "anthropic", APIKey: "wrong"},
	}}
	mc, err := executionModelConfig(c)
	if err != nil || mc.Provider != "openai" || mc.API != "responses" || mc.APIKey != "correct" || mc.Model != "custom-model" {
		t.Fatalf("connection not resolved: %v", err)
	}
	c.Provider = "missing"
	if _, err := executionModelConfig(c); err == nil {
		t.Fatal("missing connection accepted")
	}
}
