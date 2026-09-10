package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type SetProjectOverlayCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	// Rules 为空时移除 Overlay：无书级规则是默认约定，无需成文。
	Rules     []string
	Reason    string
	CreatedAt time.Time
}

type ProjectOverlayResult struct {
	ProjectID string         `json:"project_id"`
	Revision  model.Revision `json:"revision"`
	Rules     []string       `json:"rules,omitempty"`
}

// SetProjectOverlay 维护书级创作规则文档（§7.1）：真实可编辑内容，随 Project
// Revision 版本化进入提示词；修改必须由用户裁决（D31），走唯一写路径。
func (s *Catalog) SetProjectOverlay(ctx context.Context, command SetProjectOverlayCommand) (ProjectOverlayResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ProjectOverlayResult{}, fmt.Errorf("overlay change project, change, user, reason and time are required: %w", model.ErrInvalid)
	}
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return ProjectOverlayResult{}, err
	}
	ref := model.DocumentRef{Kind: model.DocumentOverlay, ID: "root"}
	existing, err := s.store.GetDocument(ctx, target, ref, revision)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return ProjectOverlayResult{}, err
	}
	hasOverlay := err == nil

	var patch model.Patch
	if len(command.Rules) == 0 {
		if !hasOverlay {
			return ProjectOverlayResult{ProjectID: command.ProjectID, Revision: revision}, nil
		}
		patch = model.Patch{Document: ref, Operation: model.PatchDelete}
	} else {
		overlay := model.ProjectOverlay{Rules: command.Rules}
		if err := overlay.Validate(); err != nil {
			return ProjectOverlayResult{}, err
		}
		if hasOverlay {
			var current model.ProjectOverlay
			if err := json.Unmarshal(existing.Content, &current); err != nil {
				return ProjectOverlayResult{}, fmt.Errorf("decode project overlay: %w", err)
			}
			if slices.Equal(current.Rules, overlay.Rules) {
				return ProjectOverlayResult{ProjectID: command.ProjectID, Revision: revision, Rules: current.Rules}, nil
			}
		}
		content, err := json.Marshal(overlay)
		if err != nil {
			return ProjectOverlayResult{}, fmt.Errorf("encode project overlay: %w", err)
		}
		patch = model.Patch{Document: ref, Operation: model.PatchPut, Content: content}
	}

	committed, err := s.changes.CommitUser(ctx, model.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []model.Patch{patch},
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return ProjectOverlayResult{}, err
	}
	return ProjectOverlayResult{ProjectID: command.ProjectID, Revision: committed.NewRevision, Rules: command.Rules}, nil
}

type SetProjectAssetsCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	// 引用 Revision 为 0 时固定为当前最新版本（用户启用即确认该版本，D31）；
	// Packs 与 CreatorProfiles 同时为空时移除全部固定引用。
	Packs           []PackRef
	CreatorProfiles []CreatorProfileRef
	Reason          string
	CreatedAt       time.Time
}

type ProjectAssetsResult struct {
	ProjectID string                 `json:"project_id"`
	Revision  model.Revision         `json:"revision"`
	Assets    model.ProjectAssetRefs `json:"assets"`
}

// SetProjectAssets 维护 Project 的跨作品资产固定引用（D31）：启用即固定版本，
// 此后 Operation 自动装配；资产本体在独立 Authority Stream，不寄生于本书。
func (s *Catalog) SetProjectAssets(ctx context.Context, command SetProjectAssetsCommand) (ProjectAssetsResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ProjectAssetsResult{}, fmt.Errorf("assets change project, change, user, reason and time are required: %w", model.ErrInvalid)
	}
	refs := model.ProjectAssetRefs{}
	for _, ref := range command.Packs {
		// 启用前解析资产本体：既校验存在性，也把浮动引用固定到确认的版本。
		pack, err := LoadPack(ctx, s.store, ref)
		if err != nil {
			return ProjectAssetsResult{}, fmt.Errorf("resolve pack %q: %w", ref.ID, err)
		}
		refs.Packs = append(refs.Packs, model.ProjectPackRef{ID: ref.ID, Revision: pack.Revision})
	}
	for _, ref := range command.CreatorProfiles {
		profile, err := LoadCreatorProfile(ctx, s.store, ref)
		if err != nil {
			return ProjectAssetsResult{}, fmt.Errorf("resolve creator profile %q/%s: %w", ref.ID, ref.Scope, err)
		}
		refs.CreatorProfiles = append(refs.CreatorProfiles, model.ProjectProfileRef{
			ID: ref.ID, Scope: ref.Scope, Revision: profile.Revision,
		})
	}
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return ProjectAssetsResult{}, err
	}
	docRef := model.DocumentRef{Kind: model.DocumentAssets, ID: "root"}
	existing, err := s.store.GetDocument(ctx, target, docRef, revision)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return ProjectAssetsResult{}, err
	}
	hasAssets := err == nil

	var patch model.Patch
	if len(refs.Packs) == 0 && len(refs.CreatorProfiles) == 0 {
		if !hasAssets {
			return ProjectAssetsResult{ProjectID: command.ProjectID, Revision: revision}, nil
		}
		patch = model.Patch{Document: docRef, Operation: model.PatchDelete}
	} else {
		if err := refs.Validate(); err != nil {
			return ProjectAssetsResult{}, err
		}
		content, err := json.Marshal(refs)
		if err != nil {
			return ProjectAssetsResult{}, fmt.Errorf("encode project asset refs: %w", err)
		}
		if hasAssets && string(existing.Content) == string(content) {
			return ProjectAssetsResult{ProjectID: command.ProjectID, Revision: revision, Assets: refs}, nil
		}
		patch = model.Patch{Document: docRef, Operation: model.PatchPut, Content: content}
	}

	committed, err := s.changes.CommitUser(ctx, model.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []model.Patch{patch},
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return ProjectAssetsResult{}, err
	}
	return ProjectAssetsResult{ProjectID: command.ProjectID, Revision: committed.NewRevision, Assets: refs}, nil
}
