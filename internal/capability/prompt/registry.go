package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Registry struct {
	store *store.Store
}

type Diagnostic struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Sources []string `json:"sources,omitempty"`
}

type Diff struct {
	LeftDigest          string    `json:"left_digest"`
	RightDigest         string    `json:"right_digest"`
	StablePrefixChanged bool      `json:"stable_prefix_changed"`
	DynamicTailChanged  bool      `json:"dynamic_tail_changed"`
	ToolsChanged        bool      `json:"tools_changed"`
	AddedSources        []Source  `json:"added_sources,omitempty"`
	RemovedSources      []Source  `json:"removed_sources,omitempty"`
	StablePrefixDiff    *TextDiff `json:"stable_prefix_diff,omitempty"`
	DynamicTailDiff     *TextDiff `json:"dynamic_tail_diff,omitempty"`
}

type TextDiff struct {
	BeforeStartLine int      `json:"before_start_line"`
	AfterStartLine  int      `json:"after_start_line"`
	Removed         []string `json:"removed,omitempty"`
	Added           []string `json:"added,omitempty"`
}

func NewRegistry(authorityStore *store.Store) *Registry {
	return &Registry{store: authorityStore}
}

// Reload compiles and persists a new immutable execution profile. Existing
// Operations keep their original digest and are not modified.
func (r *Registry) Reload(ctx context.Context, request CompileRequest, createdAt time.Time) (Compiled, error) {
	if createdAt.IsZero() {
		return Compiled{}, fmt.Errorf("execution profile creation time is required: %w", domain.ErrInvalid)
	}
	compiled, err := Compile(request)
	if err != nil {
		return Compiled{}, err
	}
	tools, err := json.Marshal(compiled.Tools)
	if err != nil {
		return Compiled{}, fmt.Errorf("encode compiled tools: %w", err)
	}
	sources, err := json.Marshal(compiled.Sources)
	if err != nil {
		return Compiled{}, fmt.Errorf("encode prompt sources: %w", err)
	}
	_, err = r.store.SaveExecutionProfile(ctx, domain.ExecutionProfileRecord{
		Digest: compiled.ProfileDigest, ProjectID: request.ProjectID,
		WorkerProfile: request.Worker.ID + "@" + request.Worker.Version,
		PromptDigest:  compiled.PromptDigest, ToolSchemaDigest: compiled.ToolSchemaDigest,
		Snapshot: compiled.Snapshot, StablePrefix: compiled.StablePrefix, DynamicTail: compiled.DynamicTail,
		Tools: tools, Sources: sources, CreatedAt: createdAt,
	})
	if err != nil {
		return Compiled{}, err
	}
	return compiled, nil
}

func (r *Registry) Load(ctx context.Context, digest string) (Compiled, error) {
	record, err := r.store.GetExecutionProfile(ctx, digest)
	if err != nil {
		return Compiled{}, err
	}
	var tools []ToolSchema
	if err := json.Unmarshal(record.Tools, &tools); err != nil {
		return Compiled{}, fmt.Errorf("decode compiled tools: %w", err)
	}
	var sources []Source
	if err := json.Unmarshal(record.Sources, &sources); err != nil {
		return Compiled{}, fmt.Errorf("decode prompt sources: %w", err)
	}
	return Compiled{
		StablePrefix: record.StablePrefix, DynamicTail: record.DynamicTail,
		Tools: tools, Sources: sources, PromptDigest: record.PromptDigest,
		ToolSchemaDigest: record.ToolSchemaDigest, ProfileDigest: record.Digest,
		Snapshot: record.Snapshot,
	}, nil
}

func (r *Registry) Show(ctx context.Context, digest string) (string, error) {
	compiled, err := r.Load(ctx, digest)
	if err != nil {
		return "", err
	}
	return compiled.StablePrefix + "\n" + compiled.DynamicTail, nil
}

func (r *Registry) Sources(ctx context.Context, digest string) ([]Source, error) {
	compiled, err := r.Load(ctx, digest)
	if err != nil {
		return nil, err
	}
	return compiled.Sources, nil
}

func (r *Registry) Diff(ctx context.Context, leftDigest, rightDigest string) (Diff, error) {
	left, err := r.Load(ctx, leftDigest)
	if err != nil {
		return Diff{}, err
	}
	right, err := r.Load(ctx, rightDigest)
	if err != nil {
		return Diff{}, err
	}
	leftSources := make(map[string]Source, len(left.Sources))
	rightSources := make(map[string]Source, len(right.Sources))
	for _, source := range left.Sources {
		leftSources[sourceKey(source)] = source
	}
	for _, source := range right.Sources {
		rightSources[sourceKey(source)] = source
	}
	result := Diff{
		LeftDigest: leftDigest, RightDigest: rightDigest,
		StablePrefixChanged: left.StablePrefix != right.StablePrefix,
		DynamicTailChanged:  right.DynamicTail != left.DynamicTail,
		ToolsChanged:        left.ToolSchemaDigest != right.ToolSchemaDigest,
		StablePrefixDiff:    diffText(left.StablePrefix, right.StablePrefix),
		DynamicTailDiff:     diffText(left.DynamicTail, right.DynamicTail),
	}
	for key, source := range rightSources {
		if _, ok := leftSources[key]; !ok {
			result.AddedSources = append(result.AddedSources, source)
		}
	}
	for key, source := range leftSources {
		if _, ok := rightSources[key]; !ok {
			result.RemovedSources = append(result.RemovedSources, source)
		}
	}
	slices.SortFunc(result.AddedSources, compareSource)
	slices.SortFunc(result.RemovedSources, compareSource)
	return result, nil
}

func diffText(before, after string) *TextDiff {
	if before == after {
		return nil
	}
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	prefix := 0
	for prefix < len(beforeLines) && prefix < len(afterLines) && beforeLines[prefix] == afterLines[prefix] {
		prefix++
	}
	beforeEnd, afterEnd := len(beforeLines), len(afterLines)
	for beforeEnd > prefix && afterEnd > prefix && beforeLines[beforeEnd-1] == afterLines[afterEnd-1] {
		beforeEnd--
		afterEnd--
	}
	return &TextDiff{
		BeforeStartLine: prefix + 1,
		AfterStartLine:  prefix + 1,
		Removed:         append([]string(nil), beforeLines[prefix:beforeEnd]...),
		Added:           append([]string(nil), afterLines[prefix:afterEnd]...),
	}
}

func (r *Registry) Lint(ctx context.Context, digest string) ([]Diagnostic, error) {
	compiled, err := r.Load(ctx, digest)
	if err != nil {
		return nil, err
	}
	separator := strings.LastIndex(compiled.Snapshot.WorkerProfileVersion, "@")
	if separator <= 0 {
		return nil, fmt.Errorf("execution profile worker version %q is invalid: %w", compiled.Snapshot.WorkerProfileVersion, domain.ErrInvalid)
	}
	workerID := compiled.Snapshot.WorkerProfileVersion[:separator]
	worker, err := BuiltinWorkerProfile(workerID)
	if err != nil || worker.ID+"@"+worker.Version != compiled.Snapshot.WorkerProfileVersion {
		return nil, fmt.Errorf("worker profile %q is unavailable: %w", compiled.Snapshot.WorkerProfileVersion, domain.ErrInvalid)
	}
	return lintSources(worker, compiled.Sources), nil
}

func Lint(request CompileRequest) ([]Diagnostic, error) {
	if _, err := Compile(request); err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(request.Packs))
	for _, pack := range request.Packs {
		slots := make([]Slot, 0, len(pack.Manifest.PromptOverlays))
		for slot := range pack.Manifest.PromptOverlays {
			slots = append(slots, Slot(slot))
		}
		slices.Sort(slots)
		sources = append(sources, Source{
			Layer: "pack_defaults", ID: pack.Manifest.ID + "@" + pack.Manifest.Version,
			Revision: pack.Revision, Kind: SourceInstruction, Slots: slots,
		})
	}
	return lintSources(request.Worker, sources), nil
}

func lintSources(worker WorkerProfile, sources []Source) []Diagnostic {
	activeSlots := make(map[string]struct{}, len(worker.PromptSlots))
	for _, slot := range worker.PromptSlots {
		activeSlots[string(slot)] = struct{}{}
	}
	owners := make(map[string][]string)
	var diagnostics []Diagnostic
	for _, source := range sources {
		if source.Layer != "pack_defaults" {
			continue
		}
		for _, promptSlot := range source.Slots {
			slot := string(promptSlot)
			if _, active := activeSlots[slot]; !active {
				diagnostics = append(diagnostics, Diagnostic{
					Code: "inactive_pack_overlay", Message: fmt.Sprintf("Pack %s 的 %s 不属于当前 Worker Profile", source.ID, slot),
					Sources: []string{source.ID},
				})
				continue
			}
			owners[slot] = append(owners[slot], source.ID)
		}
	}
	for slot, packs := range owners {
		if len(packs) < 2 {
			continue
		}
		slices.Sort(packs)
		diagnostics = append(diagnostics, Diagnostic{
			Code: "multiple_pack_overlays", Message: fmt.Sprintf("多个 Pack 向 %s 追加 Overlay；按确定性 Pack 顺序合并", slot), Sources: packs,
		})
	}
	slices.SortFunc(diagnostics, func(a, b Diagnostic) int {
		return strings.Compare(a.Code+":"+a.Message, b.Code+":"+b.Message)
	})
	return diagnostics
}

func sourceKey(source Source) string {
	return source.Layer + "\x00" + source.ID + "\x00" + fmt.Sprint(source.Revision) + "\x00" + string(source.Kind)
}

func compareSource(a, b Source) int {
	return strings.Compare(sourceKey(a), sourceKey(b))
}
