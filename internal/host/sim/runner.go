package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

const maxSourceRunes = 60000

func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourceDir) == "" {
		return nil, fmt.Errorf("source dir is required")
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}

		emit(StageScan, 0, 0, "扫描 simulate 语料...", nil)
		sources, err := scanSources(opts.SourceDir)
		if err != nil {
			emit(StageError, 0, 0, "扫描 simulate 目录失败", err)
			return
		}
		if len(sources) == 0 {
			emit(StageError, 0, 0, "simulate 目录中没有可分析的 .txt/.md/.markdown 文件", fmt.Errorf("no simulation sources"))
			return
		}

		existing, err := deps.Store.Simulation.Load()
		if err != nil {
			emit(StageError, 0, len(sources), "读取既有画像失败", err)
			return
		}
		pending := pendingSources(existing, sources)
		if len(pending) == 0 {
			emit(StageDone, 0, len(sources), "画像已是最新，未发现新增或变更文章", nil)
			return
		}

		reports := make([]domain.SimulationSourceReport, 0, len(pending))
		for i, source := range pending {
			if err := ctx.Err(); err != nil {
				emit(StageError, i, len(pending), "用户取消画像分析", err)
				return
			}
			emit(StageAnalyze, i+1, len(pending), fmt.Sprintf("分析仿写语料 %d/%d：%s", i+1, len(pending), source.RelativePath), nil)
			report, err := AnalyzeSource(ctx, deps.LLM, deps.Prompts.Source, source)
			if err != nil {
				emit(StageError, i+1, len(pending), "语料分析失败", err)
				return
			}
			reports = append(reports, *report)
		}

		allReports := mergeSourceReports(existing, reports)
		emit(StageMerge, len(pending), len(pending), "合并仿写画像...", nil)
		synthesis, err := MergeSynthesis(ctx, deps.LLM, deps.Prompts.Merge, existing, allReports)
		if err != nil {
			emit(StageError, len(pending), len(pending), "画像合并失败", err)
			return
		}
		profile := buildProfile(existing, opts.SourceDir, pending, reports, *synthesis, time.Now())
		if err := deps.Store.Simulation.Save(profile); err != nil {
			emit(StageError, len(pending), len(pending), "保存仿写画像失败", err)
			return
		}
		emit(StageDone, len(pending), len(pending), fmt.Sprintf("仿写画像已更新：新增/变更 %d 篇，累计 %d 篇", len(pending), len(profile.Corpus.Sources)), nil)
	}()
	return events, nil
}

func AnalyzeSource(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource) (*domain.SimulationSourceReport, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("source prompt is required")
	}
	report, err := generateStructured(ctx, llm, sourceReportContract, systemPrompt, buildSourceUserPrompt(source), func(report *domain.SimulationSourceReport) error {
		if strings.TrimSpace(report.Summary) == "" {
			return fmt.Errorf("summary is required")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse source report %s: %w", source.RelativePath, err)
	}
	now := time.Now().Format(time.RFC3339)
	report.RelativePath = source.RelativePath
	report.SHA256 = source.SHA256
	report.Fingerprint = source.Fingerprint
	report.AnalyzedAt = now
	return &report, nil
}

func MergeSynthesis(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) (*domain.SimulationSynthesis, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("merge prompt is required")
	}
	synthesis, err := generateStructured[domain.SimulationSynthesis](ctx, llm, synthesisContract, systemPrompt, buildMergeUserPrompt(existing, reports), nil)
	if err != nil {
		return nil, fmt.Errorf("parse synthesis: %w", err)
	}
	return &synthesis, nil
}

func generateStructured[T any](ctx context.Context, model LLMChat, contract llmcontract.Contract, systemPrompt, payload string, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, model, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Validate:     validate,
		Agent:        "simulation",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("仿写画像结构化协议选择", "contract", contract.Name,
					"structured_mode", res.Mode, "capability_source", res.Source,
					"provider", res.Provider, "model", res.Model,
					"schema_fingerprint", contract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("仿写画像输出自愈", "contract", contract.Name, "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return out, fmt.Errorf("structured generation: %w", err)
	}
	return out, nil
}

func pendingSources(existing *domain.SimulationProfile, sources []scannedSource) []scannedSource {
	if existing == nil {
		return sources
	}
	known := make(map[string]struct{}, len(existing.Corpus.Sources))
	for _, source := range existing.Corpus.Sources {
		known[domain.SimulationSourceFingerprint(source.RelativePath, source.SHA256)] = struct{}{}
	}
	var pending []scannedSource
	for _, source := range sources {
		if _, ok := known[source.Fingerprint]; ok {
			continue
		}
		pending = append(pending, source)
	}
	return pending
}

func buildProfile(
	existing *domain.SimulationProfile,
	sourceDir string,
	pending []scannedSource,
	reports []domain.SimulationSourceReport,
	synthesis domain.SimulationSynthesis,
	now time.Time,
) domain.SimulationProfile {
	stamp := now.Format(time.RFC3339)
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: stamp,
		UpdatedAt: stamp,
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: filepath.ToSlash(sourceDir),
		},
		Synthesis: synthesis,
	}
	if existing != nil {
		profile.CreatedAt = existing.CreatedAt
		if profile.CreatedAt == "" {
			profile.CreatedAt = stamp
		}
		profile.Corpus.Sources = append(profile.Corpus.Sources, existing.Corpus.Sources...)
		profile.SourceReports = append(profile.SourceReports, existing.SourceReports...)
	}

	for i, source := range pending {
		source.AnalyzedAt = stamp
		profile.Corpus.Sources = replaceSourceByPath(profile.Corpus.Sources, source.SimulationSource)
		if i < len(reports) {
			report := reports[i]
			report.AnalyzedAt = stamp
			profile.SourceReports = replaceReportByPath(profile.SourceReports, report)
		}
	}
	sortProfile(&profile)
	return profile
}

func mergeSourceReports(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) []domain.SimulationSourceReport {
	var merged []domain.SimulationSourceReport
	if existing != nil {
		merged = append(merged, existing.SourceReports...)
	}
	for _, report := range reports {
		merged = replaceReportByPath(merged, report)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].RelativePath == merged[j].RelativePath {
			return merged[i].Fingerprint < merged[j].Fingerprint
		}
		return merged[i].RelativePath < merged[j].RelativePath
	})
	return merged
}

func replaceSourceByPath(sources []domain.SimulationSource, next domain.SimulationSource) []domain.SimulationSource {
	out := sources[:0]
	for _, source := range sources {
		if source.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, source)
	}
	return append(out, next)
}

func replaceReportByPath(reports []domain.SimulationSourceReport, next domain.SimulationSourceReport) []domain.SimulationSourceReport {
	out := reports[:0]
	for _, report := range reports {
		if report.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, report)
	}
	return append(out, next)
}

func sortProfile(profile *domain.SimulationProfile) {
	sort.Slice(profile.Corpus.Sources, func(i, j int) bool {
		if profile.Corpus.Sources[i].RelativePath == profile.Corpus.Sources[j].RelativePath {
			return profile.Corpus.Sources[i].Fingerprint < profile.Corpus.Sources[j].Fingerprint
		}
		return profile.Corpus.Sources[i].RelativePath < profile.Corpus.Sources[j].RelativePath
	})
	sort.Slice(profile.SourceReports, func(i, j int) bool {
		if profile.SourceReports[i].RelativePath == profile.SourceReports[j].RelativePath {
			return profile.SourceReports[i].Fingerprint < profile.SourceReports[j].Fingerprint
		}
		return profile.SourceReports[i].RelativePath < profile.SourceReports[j].RelativePath
	})
}

func buildSourceUserPrompt(source scannedSource) string {
	payload := map[string]any{
		"relative_path": source.RelativePath,
		"sha256":        source.SHA256,
		"size_bytes":    source.SizeBytes,
		"content":       compactSourceContent(source.content),
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return string(data)
}

func buildMergeUserPrompt(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) string {
	payload := map[string]any{
		"existing_profile": domain.CompactSimulationProfile(existing),
		"source_reports":   reports,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return string(data)
}

func compactSourceContent(s string) string {
	runes := []rune(s)
	if len(runes) <= maxSourceRunes {
		return s
	}
	head := maxSourceRunes * 3 / 4
	tail := maxSourceRunes - head
	return string(runes[:head]) + "\n\n[...truncated...]\n\n" + string(runes[len(runes)-tail:])
}
