package novel

import (
	"context"
	"encoding/json"
	"slices"

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
	Verdicts []StoredVerdict
	// Prior 是基线已失效的上一轮裁定（D68）：章节正文未变时沿用其结论。
	Prior []StoredVerdict
	// Repairs 是各章当前这一版以来连续的自动修订次数，预算按章计（D63、D94）。
	Repairs map[string]int
	// Change 表示主编提出的修改正等用户确认（D77）：照着要变的内容接着写是白写，先停下。
	Change bool
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
	verdicts, prior, err := g.reader.listVerdicts(ctx, p)
	if err != nil {
		return decision, err
	}
	evidence, err := g.reader.evidence(ctx, p)
	if err != nil {
		return decision, err
	}
	evidence.Verdicts, evidence.Prior = verdicts, prior
	decision.Step, err = (Policy{}).Next(p, run, evidence)
	return decision, err
}

// RepairHold 是 run 推进卡住的那一章（Policy.RepairHold）：工作台据此让用户接受现状或跟主编说怎么改。
func (s *Reviews) RepairHold(ctx context.Context, project projectdoc.Snapshot, run model.CreationRun) (string, error) {
	verdicts, prior, err := s.listVerdicts(ctx, project)
	if err != nil {
		return "", err
	}
	evidence, err := s.evidence(ctx, project)
	if err != nil {
		return "", err
	}
	evidence.Verdicts, evidence.Prior = verdicts, prior
	return (Policy{}).RepairHold(project, run, evidence)
}

// Marks 是标记在案的章（Policy.Marks）：工作台据此在时间线上交代、主编据此知道哪些章没改好。
func (s *Reviews) Marks(ctx context.Context, project projectdoc.Snapshot, run model.CreationRun) ([]Mark, error) {
	verdicts, _, err := s.listVerdicts(ctx, project)
	if err != nil {
		return nil, err
	}
	evidence, err := s.evidence(ctx, project)
	if err != nil {
		return nil, err
	}
	evidence.Verdicts = verdicts
	return (Policy{}).Marks(project, run, evidence)
}

// evidence 读作品上等用户确认的修改与各章用掉的修订预算。预算按章计连续的自动修订
// （D63、D94）：这一章最近一次返工（没有返工时自写成）以来已派发且不再执行的重写，跨运行
// 累计。预算管的是无人过问的连续重写，新开一轮、调整策略都不让用尽的章重来；返工是用户
// 点名的改动，重新起算。
func (s *Reviews) evidence(ctx context.Context, project projectdoc.Snapshot) (Evidence, error) {
	pending, err := s.store.PendingProposals(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID})
	if err != nil {
		return Evidence{}, err
	}
	evidence := Evidence{Change: slices.ContainsFunc(pending, func(proposal model.Proposal) bool { return proposal.Author == model.ChiefAuthor })}
	operations, err := s.store.ProjectOperations(ctx, project.ID, model.OperationRewriteChapter, model.OperationReworkChapter)
	if err != nil {
		return Evidence{}, err
	}
	repairs := make(map[string]int)
	for _, op := range operations {
		input, err := model.DecodeTaskInput(op.Kind, op.Input)
		if err != nil {
			return Evidence{}, err
		}
		switch input := input.(type) {
		case *model.ReworkChapterInput:
			delete(repairs, input.ChapterID)
		case *model.RewriteChapterInput:
			// 还能接着执行的修订（含崩溃后放回队列的）不算用掉预算：推导出的仍是它，接着做完。
			if op.State != model.OperationQueued && op.State != model.OperationRunning && op.State != model.OperationPaused {
				repairs[input.ChapterID]++
			}
		}
	}
	evidence.Repairs = repairs
	return evidence, nil
}
