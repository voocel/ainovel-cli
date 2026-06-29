package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func ImportProfile(ctx context.Context, st *store.Store, path string) (ImportResult, error) {
	if st == nil {
		return ImportResult{}, fmt.Errorf("store is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ImportResult{}, fmt.Errorf("profile path is required")
	}
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImportResult{}, err
	}
	var imported domain.SimulationProfile
	if err := json.Unmarshal(data, &imported); err != nil {
		return ImportResult{}, fmt.Errorf("parse simulation profile: %w", err)
	}
	if err := domain.ValidateSimulationProfile(&imported); err != nil {
		return ImportResult{}, err
	}
	existing, err := st.Simulation.Load()
	if err != nil {
		return ImportResult{}, err
	}

	merged, result := mergeImportedProfile(existing, imported, time.Now())
	result.ProfilePath = path
	if err := st.Simulation.Save(merged); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func RunImport(ctx context.Context, st *store.Store, path string) (<-chan Event, error) {
	if st == nil {
		return nil, fmt.Errorf("store is nil")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("profile path is required")
	}
	events := make(chan Event, 8)
	go func() {
		defer close(events)
		emit := func(stage Stage, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}
		emit(StageImport, "Nhập仿写画像...", nil)
		result, err := ImportProfile(ctx, st, path)
		if err != nil {
			emit(StageError, "Nhập仿写画像Thất bại", err)
			return
		}
		emit(StageDone, fmt.Sprintf("仿写画像已Nhập：Mới增 %d 篇，Bỏ qua重复 %d 篇", result.ImportedSources, result.SkippedSources), nil)
	}()
	return events, nil
}

func mergeImportedProfile(existing *domain.SimulationProfile, imported domain.SimulationProfile, now time.Time) (domain.SimulationProfile, ImportResult) {
	stamp := now.Format(time.RFC3339)
	merged := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: imported.CreatedAt,
		UpdatedAt: stamp,
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: imported.Corpus.SourceDir,
		},
		Synthesis: imported.Synthesis,
	}
	if merged.CreatedAt == "" {
		merged.CreatedAt = stamp
	}
	if existing != nil {
		merged.CreatedAt = existing.CreatedAt
		if merged.CreatedAt == "" {
			merged.CreatedAt = stamp
		}
		merged.Corpus.SourceDir = existing.Corpus.SourceDir
		merged.Corpus.Sources = append(merged.Corpus.Sources, existing.Corpus.Sources...)
		merged.SourceReports = append(merged.SourceReports, existing.SourceReports...)
		merged.Synthesis = domain.MergeSimulationSynthesis(existing.Synthesis, imported.Synthesis)
	}

	known := make(map[string]struct{}, len(merged.Corpus.Sources))
	for _, source := range merged.Corpus.Sources {
		known[source.Fingerprint] = struct{}{}
	}
	result := ImportResult{}
	for _, source := range imported.Corpus.Sources {
		if _, ok := known[source.Fingerprint]; ok {
			result.SkippedSources++
			continue
		}
		known[source.Fingerprint] = struct{}{}
		merged.Corpus.Sources = append(merged.Corpus.Sources, source)
		result.ImportedSources++
	}

	reportKnown := make(map[string]struct{}, len(merged.SourceReports))
	for _, report := range merged.SourceReports {
		reportKnown[report.Fingerprint] = struct{}{}
	}
	for _, report := range imported.SourceReports {
		if _, ok := reportKnown[report.Fingerprint]; ok {
			continue
		}
		reportKnown[report.Fingerprint] = struct{}{}
		merged.SourceReports = append(merged.SourceReports, report)
	}
	sortProfile(&merged)
	sort.Slice(merged.SourceReports, func(i, j int) bool {
		return merged.SourceReports[i].Fingerprint < merged.SourceReports[j].Fingerprint
	})
	return merged, result
}
