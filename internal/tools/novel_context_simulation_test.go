package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
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
				Architect: []string{"escalate costs"},
				Writer:    []string{"borrow technique only"},
				Editor:    []string{"check non-copying"},
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
	if err := st.Progress.Init("test", 1, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}

	tool := NewContextTool(st, References{}, "default")
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

func TestContextToolInjectsGenreWithoutSimulationProfile(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("short", 5, domain.GenreShortStory); err != nil {
		t.Fatal(err)
	}

	tool := NewContextTool(st, References{}, "default")
	for name, args := range map[string]json.RawMessage{
		"architect": json.RawMessage(`{}`),
		"writer":    json.RawMessage(`{"chapter":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			sectionKey := "planning_memory"
			if name == "writer" {
				sectionKey = "working_memory"
			}
			section, ok := payload[sectionKey].(map[string]any)
			if !ok || section["genre"] != string(domain.GenreShortStory) {
				t.Fatalf("%s.genre = %#v", sectionKey, section["genre"])
			}
			if _, ok := payload["simulation_profile"]; ok {
				t.Fatal("测试前提不成立：未配置仿写画像时不应注入 simulation_profile")
			}
		})
	}
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
