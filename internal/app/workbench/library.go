package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type LibraryEntry struct {
	ID, Premise     string
	Written, Target int
	State           model.CreationRunState
	UpdatedAt       time.Time
}

func (s *Query) Library(ctx context.Context) ([]LibraryEntry, error) {
	summaries, err := s.store.ListProjectSummaries(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]LibraryEntry, 0, len(summaries))
	for _, summary := range summaries {
		var intent model.Intent
		if err := json.Unmarshal(summary.Intent, &intent); err != nil {
			return nil, fmt.Errorf("read library intent %s: %w", summary.ID, err)
		}
		entry := LibraryEntry{ID: summary.ID, Premise: intent.Premise, Written: summary.Written, Target: intent.TargetChapters}
		run, hasRun, err := s.runs.LatestCreationRun(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		if hasRun {
			entry.State, entry.UpdatedAt = run.State, run.UpdatedAt
			if run.Goal.Kind == model.GoalNovel {
				goal, err := model.DecodeNovelGoal(run.Goal)
				if err != nil {
					return nil, err
				}
				entry.Target = goal.TargetChapters
			}
		}
		entries = append(entries, entry)
	}
	slices.SortStableFunc(entries, func(a, b LibraryEntry) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return entries, nil
}
