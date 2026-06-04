package tui

import (
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/host"
)

func TestSimulationCommandsAreRegisteredAndNeedIdle(t *testing.T) {
	registry := commandRegistryInstance()
	for _, name := range []string{"simulate", "importsim"} {
		spec, ok := registry.Find(name)
		if !ok {
			t.Fatalf("expected /%s command to be registered", name)
		}
		if !spec.NeedsIdle {
			t.Fatalf("/%s should require idle state", name)
		}
	}

	items := builtinCommandItems()
	if !hasPaletteItem(items, "simulate") || !hasPaletteItem(items, "importsim") {
		t.Fatalf("expected simulate commands in palette: %+v", items)
	}
}

func TestSimulationCommandsAreBlockedWhileRunning(t *testing.T) {
	m := Model{snapshot: host.UISnapshot{IsRunning: true}, eventIndex: map[string]int{}}
	next, _ := m.handleSlashCommand(slashCommand{name: "simulate"})
	got := next.(Model)
	if len(got.events) != 1 || got.events[0].Category != "ERROR" {
		t.Fatalf("expected NeedsIdle to emit one error, got %+v", got.events)
	}
	if got.simulator != nil {
		t.Fatal("simulate modal should not start while runtime is running")
	}
}

func hasPaletteItem(items []commandPaletteItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}
