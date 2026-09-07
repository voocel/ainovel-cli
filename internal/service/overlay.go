package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
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
	ProjectID string          `json:"project_id"`
	Revision  domain.Revision `json:"revision"`
	Rules     []string        `json:"rules,omitempty"`
}

// SetProjectOverlay 维护书级创作规则文档（§7.1）：真实可编辑内容，随 Project
// Revision 版本化进入提示词；修改必须由用户裁决（D31），走唯一写路径。
func (s *Service) SetProjectOverlay(ctx context.Context, command SetProjectOverlayCommand) (ProjectOverlayResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ProjectOverlayResult{}, fmt.Errorf("overlay change project, change, user, reason and time are required: %w", domain.ErrInvalid)
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return ProjectOverlayResult{}, err
	}
	ref := domain.DocumentRef{Kind: domain.DocumentOverlay, ID: "root"}
	existing, err := s.store.GetDocument(ctx, target, ref, revision)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return ProjectOverlayResult{}, err
	}
	hasOverlay := err == nil

	var patch domain.Patch
	if len(command.Rules) == 0 {
		if !hasOverlay {
			return ProjectOverlayResult{ProjectID: command.ProjectID, Revision: revision}, nil
		}
		patch = domain.Patch{Document: ref, Operation: domain.PatchDelete}
	} else {
		overlay := domain.ProjectOverlay{Rules: command.Rules}
		if err := overlay.Validate(); err != nil {
			return ProjectOverlayResult{}, err
		}
		if hasOverlay {
			var current domain.ProjectOverlay
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
		patch = domain.Patch{Document: ref, Operation: domain.PatchPut, Content: content}
	}

	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []domain.Patch{patch},
		ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
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
	ProjectID string                  `json:"project_id"`
	Revision  domain.Revision         `json:"revision"`
	Assets    domain.ProjectAssetRefs `json:"assets"`
}

// SetProjectAssets 维护 Project 的跨作品资产固定引用（D31）：启用即固定版本，
// 此后 Operation 自动装配；资产本体在独立 Authority Stream，不寄生于本书。
func (s *Service) SetProjectAssets(ctx context.Context, command SetProjectAssetsCommand) (ProjectAssetsResult, error) {
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ChangeID) == "" ||
		strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Reason) == "" || command.CreatedAt.IsZero() {
		return ProjectAssetsResult{}, fmt.Errorf("assets change project, change, user, reason and time are required: %w", domain.ErrInvalid)
	}
	refs := domain.ProjectAssetRefs{}
	for _, ref := range command.Packs {
		// 启用前解析资产本体：既校验存在性，也把浮动引用固定到确认的版本。
		pack, err := loadPack(ctx, s.store, ref)
		if err != nil {
			return ProjectAssetsResult{}, fmt.Errorf("resolve pack %q: %w", ref.ID, err)
		}
		refs.Packs = append(refs.Packs, domain.ProjectPackRef{ID: ref.ID, Revision: pack.Revision})
	}
	for _, ref := range command.CreatorProfiles {
		profile, err := loadCreatorProfile(ctx, s.store, ref)
		if err != nil {
			return ProjectAssetsResult{}, fmt.Errorf("resolve creator profile %q/%s: %w", ref.ID, ref.Scope, err)
		}
		refs.CreatorProfiles = append(refs.CreatorProfiles, domain.ProjectProfileRef{
			ID: ref.ID, Scope: ref.Scope, Revision: profile.Revision,
		})
	}
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	revision, err := s.store.CurrentRevision(ctx, target)
	if err != nil {
		return ProjectAssetsResult{}, err
	}
	docRef := domain.DocumentRef{Kind: domain.DocumentAssets, ID: "root"}
	existing, err := s.store.GetDocument(ctx, target, docRef, revision)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return ProjectAssetsResult{}, err
	}
	hasAssets := err == nil

	var patch domain.Patch
	if len(refs.Packs) == 0 && len(refs.CreatorProfiles) == 0 {
		if !hasAssets {
			return ProjectAssetsResult{ProjectID: command.ProjectID, Revision: revision}, nil
		}
		patch = domain.Patch{Document: docRef, Operation: domain.PatchDelete}
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
		patch = domain.Patch{Document: docRef, Operation: domain.PatchPut, Content: content}
	}

	committed, err := s.commitUserProposal(ctx, domain.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: revision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: []domain.Patch{patch},
		ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
	}, command.CreatedAt)
	if err != nil {
		return ProjectAssetsResult{}, err
	}
	return ProjectAssetsResult{ProjectID: command.ProjectID, Revision: committed.NewRevision, Assets: refs}, nil
}
