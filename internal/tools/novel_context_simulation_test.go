package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/rules"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

func TestContextToolInjectsCompactSimulationProfile(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	profile := domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: "a.txt",
				SHA256:       "sha-a",
				Fingerprint:  domain.SimulationSourceFingerprint("a.txt", "sha-a"),
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: "a.txt",
			SHA256:       "sha-a",
			Fingerprint:  domain.SimulationSourceFingerprint("a.txt", "sha-a"),
			Summary:      "full report should not be injected",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close third"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Coordinator: []string{"keep tasks aligned"},
				Architect:   []string{"escalate costs"},
				Writer:      []string{"borrow technique only"},
				Editor:      []string{"check non-copying"},
			},
		},
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "Start", CoreEvent: "Begin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 1); err != nil {
		t.Fatal(err)
	}

	tool := NewContextTool(st, References{}, "default", rules.LoadOptions{})
	architectRaw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("architect Execute: %v", err)
	}
	var architect map[string]any
	if err := json.Unmarshal(architectRaw, &architect); err != nil {
		t.Fatal(err)
	}
	assertCompactSimulationProfile(t, architect, "planning_memory")

	chapterRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("chapter Execute: %v", err)
	}
	var chapter map[string]any
	if err := json.Unmarshal(chapterRaw, &chapter); err != nil {
		t.Fatal(err)
	}
	assertCompactSimulationProfile(t, chapter, "working_memory")
}

func assertCompactSimulationProfile(t *testing.T, payload map[string]any, section string) {
	t.Helper()
	if got := payload["simulation_profile"]; got != true {
		t.Fatalf("expected top-level simulation_profile marker, got %#v", got)
	}
	sectionMap, ok := payload[section].(map[string]any)
	if !ok {
		t.Fatalf("expected %s", section)
	}
	compact, ok := sectionMap["simulation_profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected simulation_profile under %s", section)
	}
	if _, exists := compact["source_reports"]; exists {
		t.Fatal("compact simulation_profile must not include source_reports")
	}
	if got := compact["source_count"]; got != float64(1) {
		t.Fatalf("source_count = %v, want 1", got)
	}
}
