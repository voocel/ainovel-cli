package models

import "testing"

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

func TestEndpointChangesDigest(t *testing.T) {
	c := Config{Provider: "openai", Model: "custom"}
	chat, _ := c.Digest()
	c.API = "responses"
	responses, _ := c.Digest()
	if chat == responses {
		t.Fatal("endpoint change must invalidate execution configuration")
	}
}
