package bootstrap_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func TestSetOwnershipLockGuidedAndUnlock(t *testing.T) {
	ctx := context.Background()
	api := newTestApp(openTestStore(t))
	createdAt := testTime()
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: "owned-book", ChangeID: "create", UserID: "user-1", Reason: "建立作品",
		Draft: projectdoc.ProjectDraft{
			Intent:   model.Intent{Premise: "一个失忆的邮差替亡者送完最后一封信"},
			Entities: []model.Entity{{ID: "hero", Kind: model.EntityCharacter, Name: "邮差"}},
			Canon: []model.CanonFact{{
				ID: "fact-1", Kind: model.CanonState, SubjectID: "hero",
				Predicate: "state.hero_identity", Value: json.RawMessage(`"邮差"`),
			}},
		},
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	target := model.DocumentRef{Kind: model.DocumentCanon, ID: "fact-1"}

	locked, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "lock-1", UserID: "user-1", Target: target,
		Control: model.ControlLocked, Reason: "核心设定不许动", CreatedAt: createdAt.Add(time.Minute),
	})
	if err != nil || locked.Revision != 2 || len(locked.Ownership) != 1 || locked.Ownership[0].Control != model.ControlLocked {
		t.Fatalf("lock = %#v, %v", locked, err)
	}

	// 相同锁定幂等：不产生新 Revision。
	repeated, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "lock-2", UserID: "user-1", Target: target,
		Control: model.ControlLocked, Reason: "重复锁定", CreatedAt: createdAt.Add(2 * time.Minute),
	})
	if err != nil || repeated.Revision != 2 {
		t.Fatalf("repeated lock = %#v, %v", repeated, err)
	}

	guided, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "guide-1", UserID: "user-1", Target: target,
		Control: model.ControlGuided, Guidance: []string{"身份揭示要留到第三卷"},
		Reason: "放宽为引导", CreatedAt: createdAt.Add(3 * time.Minute),
	})
	if err != nil || guided.Revision != 3 || guided.Ownership[0].Control != model.ControlGuided {
		t.Fatalf("guide = %#v, %v", guided, err)
	}

	unlocked, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "unlock-1", UserID: "user-1", Target: target,
		Reason: "交还给 AI", CreatedAt: createdAt.Add(4 * time.Minute),
	})
	if err != nil || unlocked.Revision != 4 || len(unlocked.Ownership) != 0 {
		t.Fatalf("unlock = %#v, %v", unlocked, err)
	}
	again, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "unlock-2", UserID: "user-1", Target: target,
		Reason: "重复解锁", CreatedAt: createdAt.Add(5 * time.Minute),
	})
	if err != nil || again.Revision != 4 {
		t.Fatalf("repeated unlock = %#v, %v", again, err)
	}
}
