package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSetOwnershipLockGuidedAndUnlock(t *testing.T) {
	ctx := context.Background()
	api := New(openServiceStore(t))
	createdAt := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "owned-book", ChangeID: "create", UserID: "user-1", Reason: "建立作品",
		Draft: ProjectDraft{
			Intent: domain.Intent{Premise: "一个失忆的邮差替亡者送完最后一封信"},
			Canon: []domain.CanonFact{{
				ID: "fact-1", Kind: domain.CanonState, SubjectID: "hero",
				Predicate: "state.hero_identity", Value: json.RawMessage(`"邮差"`),
			}},
		},
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	target := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "fact-1"}

	locked, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "lock-1", UserID: "user-1", Target: target,
		Control: domain.ControlLocked, Reason: "核心设定不许动", CreatedAt: createdAt.Add(time.Minute),
	})
	if err != nil || locked.Revision != 2 || len(locked.Ownership) != 1 || locked.Ownership[0].Control != domain.ControlLocked {
		t.Fatalf("lock = %#v, %v", locked, err)
	}

	// 相同锁定幂等：不产生新 Revision。
	repeated, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "lock-2", UserID: "user-1", Target: target,
		Control: domain.ControlLocked, Reason: "重复锁定", CreatedAt: createdAt.Add(2 * time.Minute),
	})
	if err != nil || repeated.Revision != 2 {
		t.Fatalf("repeated lock = %#v, %v", repeated, err)
	}

	guided, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "guide-1", UserID: "user-1", Target: target,
		Control: domain.ControlGuided, Guidance: []string{"身份揭示要留到第三卷"},
		Reason: "放宽为引导", CreatedAt: createdAt.Add(3 * time.Minute),
	})
	if err != nil || guided.Revision != 3 || guided.Ownership[0].Control != domain.ControlGuided {
		t.Fatalf("guide = %#v, %v", guided, err)
	}

	unlocked, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "unlock-1", UserID: "user-1", Target: target,
		Reason: "交还给 AI", CreatedAt: createdAt.Add(4 * time.Minute),
	})
	if err != nil || unlocked.Revision != 4 || len(unlocked.Ownership) != 0 {
		t.Fatalf("unlock = %#v, %v", unlocked, err)
	}
	again, err := api.SetOwnership(ctx, SetOwnershipCommand{
		ProjectID: "owned-book", ChangeID: "unlock-2", UserID: "user-1", Target: target,
		Reason: "重复解锁", CreatedAt: createdAt.Add(5 * time.Minute),
	})
	if err != nil || again.Revision != 4 {
		t.Fatalf("repeated unlock = %#v, %v", again, err)
	}
}
