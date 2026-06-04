package sim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

func TestImportProfileValidatesSchemaAndMergesByFingerprint(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(filepath.Join(dir, "output", "novel"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	existing := testProfile("a.txt", "sha-a", "old")
	if err := st.Simulation.Save(existing); err != nil {
		t.Fatal(err)
	}

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"version":"wrong"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProfile(context.Background(), st, badPath); err == nil || !strings.Contains(err.Error(), "unsupported simulation profile") {
		t.Fatalf("expected schema validation error, got %v", err)
	}

	imported := testProfile("b.txt", "sha-b", "new")
	imported.Corpus.Sources = append(imported.Corpus.Sources, existing.Corpus.Sources[0])
	imported.SourceReports = append(imported.SourceReports, existing.SourceReports[0])
	importPath := filepath.Join(dir, "profile.json")
	data, err := domain.MarshalSimulationProfile(imported)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(importPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportProfile(context.Background(), st, importPath)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	if result.ImportedSources != 1 || result.SkippedSources != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	merged, err := st.Simulation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Corpus.Sources) != 2 || len(merged.SourceReports) != 2 {
		t.Fatalf("expected duplicate fingerprint to be skipped, got %+v", merged)
	}
}

func testProfile(path, sha, summary string) domain.SimulationProfile {
	fp := domain.SimulationSourceFingerprint(path, sha)
	return domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: path,
				SHA256:       sha,
				Fingerprint:  fp,
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: path,
			SHA256:       sha,
			Fingerprint:  fp,
			Summary:      summary,
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close narration"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Writer: []string{"borrow structure only"},
			},
		},
	}
}
