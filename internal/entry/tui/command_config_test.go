package tui

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

func TestParseContextWindowInput(t *testing.T) {
	cases := map[string]int{
		"": 0, "0": 0, "auto": 0, "128K": 128000, "1M": 1000000,
		"1.5m": 1500000, "200000": 200000,
	}
	for input, want := range cases {
		got, err := parseContextWindowInput(input)
		if err != nil || got != want {
			t.Errorf("parseContextWindowInput(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "abc", "0.5"} {
		if _, err := parseContextWindowInput(input); err == nil {
			t.Errorf("parseContextWindowInput(%q) should fail", input)
		}
	}
}

func TestModelConfigModalDoesNotRenderAPIKey(t *testing.T) {
	state := &modelConfigState{step: configStepKeyInput, input: "sk-super-secret"}
	view := renderModelConfigModal(120, 40, state)
	if strings.Contains(view, "sk-super-secret") {
		t.Fatal("API key leaked into rendered modal")
	}
}

func TestConfigCommandIsRegistered(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("config")
	if !ok {
		t.Fatal("/config is not registered")
	}
	if spec.Usage != "/config" || !spec.AutoExecute {
		t.Fatalf("config spec = %#v", spec)
	}
}

func TestModelSwitchLabelIncludesContextWindow(t *testing.T) {
	state := modelSwitchState{models: []host.ConfiguredModel{{Name: "gpt-test", ContextWindow: 400000}}}
	if got := state.modelLabel(); got != "gpt-test · 400K" {
		t.Fatalf("modelLabel = %q", got)
	}
}
