package project

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

const (
	fixtureProject = "book-1"
	fixtureUser    = "user-1"
)

type projectionFixture struct {
	ctx  context.Context
	repo *Repository
	now  time.Time
}

// newProjectionFixture 建一本已提交的作品：一卷一弧一章蓝图。
func newProjectionFixture(t *testing.T) *projectionFixture {
	t.Helper()
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { authorityStore.Close() })
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	repo := New(authorityStore, change.New(authorityStore))
	if _, err := repo.CreateProject(ctx, CreateProjectCommand{
		ProjectID: fixtureProject, ChangeID: "create", UserID: fixtureUser, Reason: "创建作品",
		Draft: ProjectDraft{
			Intent: model.Intent{Premise: "凡人修仙", TargetChapters: 1},
			Plan: []model.PlanNode{
				{ID: "v1", Kind: model.PlanVolume, Title: "第一卷", Summary: "起"},
				{ID: "a1", ParentID: "v1", Kind: model.PlanArc, Title: "第一弧", Summary: "承"},
				{ID: "c1", ParentID: "a1", Kind: model.PlanChapter, Title: "第一章", Summary: "入门"},
			},
		},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return &projectionFixture{ctx: ctx, repo: repo, now: now}
}

func (f *projectionFixture) export(t *testing.T) ProjectProjection {
	t.Helper()
	projection, err := f.repo.ExportProject(f.ctx, fixtureProject, model.InitialRevision)
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	return projection
}

// 导入是"把作品改成投影所描述的样子"：差异变成补丁，同样的投影不产生补丁。
func TestImportProjectDiffsAgainstCurrentState(t *testing.T) {
	f := newProjectionFixture(t)
	base := f.export(t)

	cases := map[string]struct {
		edit      func(ProjectProjection) ProjectProjection
		wantKinds map[model.DocumentKind]model.PatchOperation
	}{
		"改标题生成 put": {
			edit: func(p ProjectProjection) ProjectProjection {
				p.Plan[2].Title = "改名后的第一章"
				return p
			},
			wantKinds: map[model.DocumentKind]model.PatchOperation{model.DocumentPlan: model.PatchPut},
		},
		"删节点生成 delete": {
			edit: func(p ProjectProjection) ProjectProjection {
				kept := p.Plan[:0]
				for _, node := range p.Plan {
					if node.ID != "c1" { // 去掉叶子章节，保留父节点避免结构冲突
						kept = append(kept, node)
					}
				}
				p.Plan = kept
				return p
			},
			wantKinds: map[model.DocumentKind]model.PatchOperation{model.DocumentPlan: model.PatchDelete},
		},
		"改意图生成 put": {
			edit: func(p ProjectProjection) ProjectProjection {
				p.Intent.Premise = "凡人修仙（修订）"
				return p
			},
			wantKinds: map[model.DocumentKind]model.PatchOperation{model.DocumentIntent: model.PatchPut},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			proposal, err := f.repo.ImportProject(f.ctx, "import-"+name, fixtureUser, "导入", tc.edit(f.export(t)), f.now)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if len(proposal.Patches) == 0 {
				t.Fatal("导入没有产生补丁")
			}
			if proposal.BaseRevision != base.BaseRevision {
				t.Errorf("基线 = %d, want %d", proposal.BaseRevision, base.BaseRevision)
			}
			if proposal.Author.Kind != model.AuthorUser || proposal.Author.ID != fixtureUser {
				t.Errorf("作者 = %#v，导入必须是用户提案", proposal.Author)
			}
			if proposal.ApprovalState != model.ApprovalPending {
				t.Errorf("审批状态 = %q，导入只产生待裁决提案", proposal.ApprovalState)
			}
			for kind, operation := range tc.wantKinds {
				found := false
				for _, patch := range proposal.Patches {
					if patch.Document.Kind == kind && patch.Operation == operation {
						found = true
					}
				}
				if !found {
					t.Errorf("补丁里没有 %s 的 %s；实际 %#v", kind, operation, proposal.Patches)
				}
			}
		})
	}
}

// 原样重导没有差异：投影不是第二事实源，不能凭空造出一次修订（D07）。
func TestImportProjectRejectsUnchangedProjection(t *testing.T) {
	f := newProjectionFixture(t)
	_, err := f.repo.ImportProject(f.ctx, "import-same", fixtureUser, "重导", f.export(t), f.now)
	if !errors.Is(err, change.ErrInvalidState) {
		t.Fatalf("error = %v, want ErrInvalidState", err)
	}
}

func TestImportProjectRejectsInvalidIdentity(t *testing.T) {
	f := newProjectionFixture(t)
	cases := map[string]func(ProjectProjection) ProjectProjection{
		"缺作品 ID": func(p ProjectProjection) ProjectProjection { p.ProjectID = ""; return p },
		"缺基线":    func(p ProjectProjection) ProjectProjection { p.BaseRevision = model.InitialRevision; return p },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			projection := edit(f.export(t))
			projection.Intent.Premise = "改一下，确保不是因为无差异才失败"
			if _, err := f.repo.ImportProject(f.ctx, "import-bad", fixtureUser, "导入", projection, f.now); !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// 未知作品的导入按基线读取失败，不能静默建出一本新书。
func TestImportProjectRejectsUnknownProject(t *testing.T) {
	f := newProjectionFixture(t)
	projection := f.export(t)
	projection.ProjectID = "ghost"
	if _, err := f.repo.ImportProject(f.ctx, "import-ghost", fixtureUser, "导入", projection, f.now); err == nil {
		t.Fatal("未知作品的导入必须失败")
	}
}

// ImportNewProject 建新书：先建权威流，再把其余内容作为一份草案导入。
func TestImportNewProjectCreatesEveryDocument(t *testing.T) {
	f := newProjectionFixture(t)
	projection := f.export(t)
	projection.ProjectID = "book-2"
	projection.Approval = model.ApprovalAuto // 投影不带审批策略时，建库写入的默认值会被差异删掉
	proposal, err := f.repo.ImportNewProject(f.ctx, "import-new", fixtureUser, "导入新书", projection, f.now)
	if err != nil {
		t.Fatalf("import new project: %v", err)
	}
	// ImportNewProject 先用 Intent/Approval 建流，再把其余内容作为一份待批准草案导入，
	// 因此基线是刚建出的那一版，补丁只增不删。
	if proposal.BaseRevision <= model.InitialRevision {
		t.Errorf("新书基线 = %d，应是刚建出的版本", proposal.BaseRevision)
	}
	for _, patch := range proposal.Patches {
		if patch.Operation != model.PatchPut {
			t.Errorf("新书导入含非 put 补丁 %#v", patch)
		}
	}
	if proposal.Target.ID != "book-2" {
		t.Errorf("目标 = %q, want book-2", proposal.Target.ID)
	}
}
