package domain

import (
	"strconv"
	"testing"
)

func TestCompactSimulationProfileCapsInjectedArrays(t *testing.T) {
	profile := &SimulationProfile{
		Version: SimulationProfileVersion,
		Corpus: SimulationCorpusManifest{
			Sources: make([]SimulationSource, 25),
		},
		Synthesis: SimulationSynthesis{
			Style: SimulationStyle{
				NarrativeVoice: longSimulationList("voice", maxCompactSimulationItems+5),
				DoNotCopy:      longSimulationList("copy", maxCompactSimulationItems+5),
			},
			Lexicon: SimulationLexicon{
				CommonWords: longSimulationList("word", maxCompactSimulationItems+5),
			},
			PlotDesign: SimulationPlotDesign{
				OpeningPatterns: longSimulationList("opening", maxCompactSimulationItems+5),
			},
			HookDesign: SimulationHookDesign{
				HookTypes: longSimulationList("hook", maxCompactSimulationItems+5),
			},
			PacingDensity: SimulationPacingDensity{
				SceneDensity: longSimulationList("density", maxCompactSimulationItems+5),
			},
			ReaderEngagement: SimulationReaderEngagement{
				Methods: longSimulationList("method", maxCompactSimulationItems+5),
			},
			RoleGuidance: SimulationRoleGuidance{
				Writer: longSimulationList("writer", maxCompactSimulationItems+5),
			},
		},
	}
	for i := range profile.Corpus.Sources {
		profile.Corpus.Sources[i] = SimulationSource{RelativePath: "source-" + strconv.Itoa(i)}
	}

	compact := CompactSimulationProfile(profile)
	if compact == nil {
		t.Fatal("compact profile is nil")
	}
	if got := len(compact.SourceFiles); got != maxCompactSimulationSourceFiles {
		t.Fatalf("SourceFiles len = %d, want %d", got, maxCompactSimulationSourceFiles)
	}
	assertCompactLen(t, "Style.NarrativeVoice", compact.Style.NarrativeVoice)
	assertCompactLen(t, "Style.DoNotCopy", compact.Style.DoNotCopy)
	assertCompactLen(t, "Lexicon.CommonWords", compact.Lexicon.CommonWords)
	assertCompactLen(t, "PlotDesign.OpeningPatterns", compact.PlotDesign.OpeningPatterns)
	assertCompactLen(t, "HookDesign.HookTypes", compact.HookDesign.HookTypes)
	assertCompactLen(t, "PacingDensity.SceneDensity", compact.PacingDensity.SceneDensity)
	assertCompactLen(t, "ReaderEngagement.Methods", compact.ReaderEngagement.Methods)
	assertCompactLen(t, "RoleGuidance.Writer", compact.RoleGuidance.Writer)
	if got := len(profile.Synthesis.Style.NarrativeVoice); got != maxCompactSimulationItems+5 {
		t.Fatalf("CompactSimulationProfile mutated source profile, len = %d", got)
	}
}

func assertCompactLen(t *testing.T, name string, got []string) {
	t.Helper()
	if len(got) != maxCompactSimulationItems {
		t.Fatalf("%s len = %d, want %d", name, len(got), maxCompactSimulationItems)
	}
}

func longSimulationList(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + strconv.Itoa(i)
	}
	return out
}
