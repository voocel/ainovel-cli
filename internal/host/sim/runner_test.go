package sim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type scriptedLLM struct {
	responses []string
	calls     atomic.Int32
}

func (s *scriptedLLM) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := int(s.calls.Add(1)) - 1
	if idx >= len(s.responses) {
		return nil, fmt.Errorf("scriptedLLM exhausted at call %d", idx+1)
	}
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:      agentcore.RoleAssistant,
			Content:   []agentcore.ContentBlock{agentcore.TextBlock(s.responses[idx])},
			Timestamp: time.Now(),
		},
	}, nil
}

func TestRunnerGeneratesProfileThenSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("A tense opening hook.\nA quick reveal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "nested", "b.md"), []byte("# B\n\nSecond sample with cliffhanger."), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedLLM{responses: []string{
		validSourceReportJSON("a tone"),
		validSourceReportJSON("b tone"),
		validSynthesisJSON("tight pacing"),
	}}

	events, err := Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var last Event
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("simulate errored: %v", ev.Err)
		}
		last = ev
	}
	if last.Stage != StageDone {
		t.Fatalf("last stage = %s, want %s", last.Stage, StageDone)
	}
	if got := llm.calls.Load(); got != 3 {
		t.Fatalf("first run LLM calls = %d, want 3", got)
	}
	profile, err := st.Simulation.Load()
	if err != nil {
		t.Fatalf("Load profile: %v", err)
	}
	if profile == nil || len(profile.Corpus.Sources) != 2 || len(profile.SourceReports) != 2 {
		t.Fatalf("profile not persisted with two sources: %+v", profile)
	}

	llm2 := &scriptedLLM{}
	events, err = Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm2,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("rerun Run: %v", err)
	}
	var upToDate bool
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("rerun errored: %v", ev.Err)
		}
		if strings.Contains(ev.Message, "画像已是最Mới") {
			upToDate = true
		}
	}
	if !upToDate {
		t.Fatal("expected up-to-date message")
	}
	if got := llm2.calls.Load(); got != 0 {
		t.Fatalf("unchanged rerun LLM calls = %d, want 0", got)
	}
}

func TestRunnerIncrementallyAnalyzesNewAndChangedSources(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "simulate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(sourceDir, "a.txt")
	if err := os.WriteFile(aPath, []byte("first version"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	firstLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("first tone"),
		validSynthesisJSON("first synthesis"),
	}}
	drainRun(t, st, firstLLM, sourceDir)
	if got := firstLLM.calls.Load(); got != 2 {
		t.Fatalf("first run calls = %d, want 2", got)
	}

	if err := os.WriteFile(filepath.Join(sourceDir, "c.markdown"), []byte("new material"), 0o644); err != nil {
		t.Fatal(err)
	}
	addLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("new tone"),
		validSynthesisJSON("expanded synthesis"),
	}}
	drainRun(t, st, addLLM, sourceDir)
	if got := addLLM.calls.Load(); got != 2 {
		t.Fatalf("new source run calls = %d, want 2", got)
	}
	profile, _ := st.Simulation.Load()
	if len(profile.Corpus.Sources) != 2 {
		t.Fatalf("after adding source, source count = %d, want 2", len(profile.Corpus.Sources))
	}

	oldHash := sourceHashForPath(t, profile, "a.txt")
	if err := os.WriteFile(aPath, []byte("changed version"), 0o644); err != nil {
		t.Fatal(err)
	}
	changeLLM := &scriptedLLM{responses: []string{
		validSourceReportJSON("changed tone"),
		validSynthesisJSON("changed synthesis"),
	}}
	drainRun(t, st, changeLLM, sourceDir)
	if got := changeLLM.calls.Load(); got != 2 {
		t.Fatalf("changed source run calls = %d, want 2", got)
	}
	profile, _ = st.Simulation.Load()
	if len(profile.Corpus.Sources) != 2 {
		t.Fatalf("changed source should replace same path, got %d sources", len(profile.Corpus.Sources))
	}
	if newHash := sourceHashForPath(t, profile, "a.txt"); newHash == oldHash {
		t.Fatal("expected changed source hash to update")
	}
}

func drainRun(t *testing.T, st *store.Store, llm *scriptedLLM, sourceDir string) {
	t.Helper()
	events, err := Run(context.Background(), Deps{
		Store:   st,
		LLM:     llm,
		Prompts: Prompts{Source: "source prompt", Merge: "merge prompt"},
	}, Options{SourceDir: sourceDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("simulate errored: %v", ev.Err)
		}
	}
}

func sourceHashForPath(t *testing.T, profile *domain.SimulationProfile, rel string) string {
	t.Helper()
	for _, source := range profile.Corpus.Sources {
		if source.RelativePath == rel {
			return source.SHA256
		}
	}
	t.Fatalf("source %q not found", rel)
	return ""
}

func validSourceReportJSON(summary string) string {
	return fmt.Sprintf(`{
  "summary": %q,
  "style_observations": ["close perspective", "sensory verbs"],
  "common_words": ["door", "shadow"],
  "plot_patterns": ["scene goal turns into a sharper dilemma"],
  "hook_patterns": ["end with an unanswered choice"],
  "pacing_notes": ["short setup, fast complication"],
  "reader_appeal": ["curiosity gap", "clear stakes"],
  "reusable_techniques": ["plant a concrete object before the reveal"]
}`, summary)
}

func validSynthesisJSON(note string) string {
	return fmt.Sprintf(`{
  "style": {
    "narrative_voice": ["limited close narration"],
    "sentence_rhythm": ["mix short impact lines with medium action lines"],
    "prose_texture": [%q],
    "perspective": ["stay near the protagonist"],
    "mood": ["tense, urgent"],
    "do_not_copy": ["do not reuse original names or sentences"]
  },
  "lexicon": {
    "common_words": ["door", "shadow"],
    "emotion_words": ["hesitation"],
    "scene_words": ["corridor"],
    "transition_words": ["meanwhile"],
    "signature_phrases": ["not yet"]
  },
  "plot_design": {
    "opening_patterns": ["start inside an unresolved pressure"],
    "escalation_patterns": ["make the cost visible after each answer"],
    "turning_point_patterns": ["reframe the clue"],
    "payoff_patterns": ["pay off the object before adding the next question"]
  },
  "hook_design": {
    "hook_types": ["mystery", "choice"],
    "placement": ["open and close scenes with changed stakes"],
    "cliffhanger_patterns": ["choice before consequence"],
    "payoff_rules": ["answer one question while opening another"]
  },
  "pacing_density": {
    "scene_density": ["one scene should carry goal, obstacle, turn"],
    "information_release": ["delay explanation until after action"],
    "dialogue_action_ratio": ["dialogue must change leverage"],
    "compression_rules": ["summarize transit, dramatize decisions"]
  },
  "reader_engagement": {
    "methods": ["curiosity gap", "stakes"],
    "emotional_drivers": ["fear of loss"],
    "progression_rewards": ["visible clue gain"],
    "anti_patterns": ["flat exposition"]
  },
  "role_guidance": {
    "coordinator": ["keep later tasks aligned with the simulation profile"],
    "architect": ["design arcs with repeated cost escalation"],
    "writer": ["borrow techniques, never copy text"],
    "editor": ["check imitation stays structural rather than plagiaristic"]
  }
}`, note)
}
