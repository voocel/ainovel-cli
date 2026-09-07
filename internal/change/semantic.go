package change

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type SemanticImpactStatus string

const (
	SemanticImpactConsistent SemanticImpactStatus = "consistent"
	SemanticImpactConflict   SemanticImpactStatus = "conflict"
	SemanticImpactUncertain  SemanticImpactStatus = "uncertain"
)

type ResolutionStrategy string

const (
	ResolutionRewriteAffected   ResolutionStrategy = "rewrite_affected"
	ResolutionReinterpretFuture ResolutionStrategy = "reinterpret_future"
	ResolutionAbandon           ResolutionStrategy = "abandon"
)

type SemanticImpactFinding struct {
	Document    *domain.DocumentRef `json:"document,omitempty"`
	Explanation string              `json:"explanation"`
}

type ResolutionOption struct {
	Strategy    ResolutionStrategy `json:"strategy"`
	ChapterIDs  []string           `json:"chapter_ids,omitempty"`
	Explanation string             `json:"explanation"`
}

type SemanticImpactReport struct {
	Status   SemanticImpactStatus    `json:"status"`
	Findings []SemanticImpactFinding `json:"findings"`
	Options  []ResolutionOption      `json:"options"`
}

func (r SemanticImpactReport) Validate() error {
	switch r.Status {
	case SemanticImpactConsistent:
		if len(r.Findings) != 0 || len(r.Options) != 0 {
			return fmt.Errorf("consistent semantic impact cannot contain findings or resolution options: %w", domain.ErrInvalid)
		}
		return nil
	case SemanticImpactConflict, SemanticImpactUncertain:
		if len(r.Findings) == 0 {
			return fmt.Errorf("semantic conflict or uncertainty requires findings: %w", domain.ErrInvalid)
		}
	default:
		return fmt.Errorf("unknown semantic impact status %q: %w", r.Status, domain.ErrInvalid)
	}
	for index, finding := range r.Findings {
		if strings.TrimSpace(finding.Explanation) == "" {
			return fmt.Errorf("semantic finding %d requires an explanation: %w", index, domain.ErrInvalid)
		}
		if finding.Document != nil {
			if err := finding.Document.Validate(); err != nil {
				return err
			}
		}
	}
	required := map[ResolutionStrategy]bool{
		ResolutionRewriteAffected: false, ResolutionReinterpretFuture: false, ResolutionAbandon: false,
	}
	for index, option := range r.Options {
		if _, ok := required[option.Strategy]; !ok || required[option.Strategy] {
			return fmt.Errorf("resolution option %d has an unknown or duplicate strategy %q: %w", index, option.Strategy, domain.ErrInvalid)
		}
		if strings.TrimSpace(option.Explanation) == "" {
			return fmt.Errorf("resolution option %q requires an explanation: %w", option.Strategy, domain.ErrInvalid)
		}
		if option.Strategy == ResolutionRewriteAffected && len(option.ChapterIDs) == 0 {
			return fmt.Errorf("rewrite_affected requires chapter_ids: %w", domain.ErrInvalid)
		}
		seen := make(map[string]struct{}, len(option.ChapterIDs))
		for _, id := range option.ChapterIDs {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("resolution chapter id is required: %w", domain.ErrInvalid)
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("duplicate resolution chapter %q: %w", id, domain.ErrInvalid)
			}
			seen[id] = struct{}{}
		}
		required[option.Strategy] = true
	}
	for strategy, present := range required {
		if !present {
			return fmt.Errorf("semantic impact is missing resolution strategy %q: %w", strategy, domain.ErrInvalid)
		}
	}
	return nil
}
