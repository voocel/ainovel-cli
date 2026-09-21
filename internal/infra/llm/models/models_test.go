package models

import (
	"testing"

	"github.com/voocel/agentcore"
)

func TestDigestTracksExecutionConfigWithoutLeakingAPIKey(t *testing.T) {
	first, err := (Config{Provider: "deepseek", Model: "deepseek-chat", APIKey: "secret-a"}).Digest()
	if err != nil {
		t.Fatalf("first digest: %v", err)
	}
	second, err := (Config{Provider: "deepseek", Model: "deepseek-chat", APIKey: "secret-b"}).Digest()
	if err != nil {
		t.Fatalf("second digest: %v", err)
	}
	if first != second {
		t.Fatal("API key rotation changed the semantic model config digest")
	}
	changed, err := (Config{Provider: "deepseek", Model: "deepseek-reasoner", APIKey: "secret-a"}).Digest()
	if err != nil {
		t.Fatalf("changed digest: %v", err)
	}
	if changed == first {
		t.Fatal("model change did not change the config digest")
	}
}

func TestThinkingLevelChangesDigestAndBinds(t *testing.T) {
	c := Config{Provider: "openai", Model: "custom", APIKey: "k"}
	auto, _ := c.Digest()
	c.Thinking = agentcore.ThinkingHigh
	high, _ := c.Digest()
	if auto == high {
		t.Fatal("thinking level must be part of the execution configuration")
	}
	binding, err := Bind(c)
	if err != nil || binding.Chat == nil || binding.Digest != high || binding.Thinking != agentcore.ThinkingHigh || binding.Model != "custom" {
		t.Fatalf("binding = %#v, %v", binding, err)
	}
	roles := Bindings{Default: binding, Roles: map[string]Binding{"writer": {Model: "other"}}}
	if roles.For("writer").Model != "other" || roles.For("editor").Model != "custom" {
		t.Fatal("role lookup must fall back to the default binding")
	}
}

func TestEndpointChangesDigest(t *testing.T) {
	c := Config{Provider: "openai", Model: "custom"}
	chat, _ := c.Digest()
	c.API = "responses"
	responses, _ := c.Digest()
	if chat == responses {
		t.Fatal("endpoint change must invalidate execution configuration")
	}
}
