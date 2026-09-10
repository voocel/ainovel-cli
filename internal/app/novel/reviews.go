package novel

import (
	"context"
	"encoding/json"
	"fmt"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Reviews owns novel review interpretation and user adjudications.
type Reviews struct {
	store    *store.Store
	changes  *change.Engine
	projects *projectdoc.Repository
}

func NewReviews(s *store.Store, changes *change.Engine, projects *projectdoc.Repository) *Reviews {
	return &Reviews{store: s, changes: changes, projects: projects}
}

type Evidence struct {
	Verdicts    []StoredVerdict
	RepairsUsed int
}

// Goal is the state-loading adapter; Policy remains a pure novel decision function.
type Goal struct{ reader *Reviews }

func NewGoal(reader *Reviews) *Goal                        { return &Goal{reader: reader} }
func (g *Goal) ValidateGoal(payload json.RawMessage) error { return (Policy{}).ValidateGoal(payload) }
func (g *Goal) Next(ctx context.Context, run model.CreationRun) (creation.Decision, error) {
	p, err := g.reader.projects.Project(ctx, run.ProjectID, 0)
	if err != nil {
		return creation.Decision{}, err
	}
	decision := creation.Decision{Revision: p.Revision}
	verdicts, err := g.reader.ListVerdicts(ctx, p)
	if err != nil {
		decision.Step.Fail = "审阅结果缺失或损坏，需要人工检查"
		return decision, err
	}
	repairs, err := g.reader.repairCount(ctx, run.ID)
	if err != nil {
		return decision, err
	}
	decision.Step, err = (Policy{}).Next(p, run, Evidence{Verdicts: verdicts, RepairsUsed: repairs})
	return decision, err
}
func (s *Reviews) repairCount(ctx context.Context, runID string) (int, error) {
	events, err := s.store.ListCreationRunEvents(ctx, runID)
	if err != nil {
		return 0, err
	}
	repairs := 0
	for _, event := range events {
		if event.Kind != model.RunEventOperationCreated {
			continue
		}
		var payload struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return 0, fmt.Errorf("run %q event %d payload is corrupt: %w", runID, event.Sequence, err)
		}
		op, err := s.store.GetOperation(ctx, payload.OperationID)
		if err != nil {
			return 0, err
		}
		spec, err := model.KindSpec(op.Kind)
		if err != nil {
			return 0, err
		}
		if spec.Repair {
			repairs++
		}
	}
	return repairs, nil
}
