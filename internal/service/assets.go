package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	packloader "github.com/voocel/ainovel-cli/internal/capability/pack"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type PackRef struct {
	ID       string          `json:"id"`
	Revision domain.Revision `json:"revision,omitempty"`
}

type CreatorProfileRef struct {
	ID       string          `json:"id"`
	Scope    string          `json:"scope"`
	Revision domain.Revision `json:"revision,omitempty"`
}

type InstalledPack struct {
	Manifest domain.PackManifest `json:"manifest"`
	Revision domain.Revision     `json:"revision"`
	Digest   string              `json:"digest"`
	Source   string              `json:"source"`
}

func (s *Service) EvaluatePack(ctx context.Context, ref PackRef, output string) (packloader.EvalResult, error) {
	pack, err := loadPack(ctx, s.store, ref)
	if err != nil {
		return packloader.EvalResult{}, err
	}
	return packloader.RunEvals(pack.Manifest, output)
}

type preferenceAnalyzer interface {
	AnalyzePreference(context.Context, domain.PreferenceLearningInput) (domain.PreferenceCandidate, error)
}

type LearnPreferenceCommand struct {
	CandidateID  string
	ProfileID    string
	Scope        string
	ProjectID    string
	FromRevision domain.Revision
	ToRevision   domain.Revision
	CreatedAt    time.Time
}

func (s *Service) LearnPreference(ctx context.Context, command LearnPreferenceCommand) (store.PreferenceCandidateRecord, error) {
	analyzer, ok := s.executor.(preferenceAnalyzer)
	if !ok {
		return store.PreferenceCandidateRecord{}, fmt.Errorf("configured runtime does not provide preference analysis: %w", domain.ErrInvalid)
	}
	before, err := s.Project(ctx, command.ProjectID, command.FromRevision)
	if err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	after, err := s.Project(ctx, command.ProjectID, command.ToRevision)
	if err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	edits, err := s.userManuscriptEdits(ctx, command.ProjectID, before.Revision, after.Revision)
	if err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	if len(edits) == 0 {
		return store.PreferenceCandidateRecord{}, fmt.Errorf(
			"revisions %d..%d contain no direct user manuscript edits to learn from: %w",
			before.Revision, after.Revision, domain.ErrInvalid,
		)
	}
	input := domain.PreferenceLearningInput{
		CandidateID: command.CandidateID, ProfileID: command.ProfileID, Scope: command.Scope,
		ProjectID: command.ProjectID, FromRevision: before.Revision, ToRevision: after.Revision,
		ManuscriptEdits: edits,
	}
	if err := input.Validate(); err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	candidate, err := analyzer.AnalyzePreference(ctx, input)
	if err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	candidate.ID = input.CandidateID
	candidate.SourceProjectID = input.ProjectID
	candidate.SourceRevision = input.ToRevision
	if err := candidate.Validate(); err != nil {
		return store.PreferenceCandidateRecord{}, err
	}
	return s.store.SavePreferenceCandidate(ctx, command.ProfileID, command.Scope, candidate, command.CreatedAt)
}

func (s *Service) PreferenceCandidates(ctx context.Context, profileID, scope string) ([]store.PreferenceCandidateRecord, error) {
	return s.store.ListPreferenceCandidates(ctx, profileID, scope)
}

// 学习证据上限：超限时按“最近优先”截断，避免长区间把上下文打爆。
const maxPreferenceEdits = 200

// userManuscriptEdits 逐个 Revision 走 ChangeSet，只收集用户直接修改（作者为
// user 且不归属任何 Operation）产生的正文编辑；AI 提交一律排除，防止把 AI 自己
// 的文风学成“用户偏好”形成自我强化回路。
func (s *Service) userManuscriptEdits(
	ctx context.Context,
	projectID string,
	from, to domain.Revision,
) ([]domain.ManuscriptEditEvidence, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projectID}
	edits := make([]domain.ManuscriptEditEvidence, 0)
	for revision := from + 1; revision <= to; revision++ {
		changeSet, err := s.store.GetChangeSetByRevision(ctx, target, revision)
		if err != nil {
			return nil, err
		}
		if changeSet.Author.Kind != domain.AuthorUser || changeSet.OperationID != "" {
			continue
		}
		var beforeChapters, afterChapters []domain.ManuscriptChapter
		for _, patch := range changeSet.Patches {
			if patch.Document.Kind != domain.DocumentManuscript {
				continue
			}
			if changeSet.BaseRevision > domain.InitialRevision {
				previous, err := s.store.GetDocument(ctx, target, patch.Document, changeSet.BaseRevision)
				if err == nil {
					var chapter domain.ManuscriptChapter
					if err := json.Unmarshal(previous.Content, &chapter); err != nil {
						return nil, fmt.Errorf("decode chapter %q before edit: %w", patch.Document.ID, err)
					}
					beforeChapters = append(beforeChapters, chapter)
				} else if !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
			}
			if patch.Operation == domain.PatchPut {
				var chapter domain.ManuscriptChapter
				if err := json.Unmarshal(patch.Content, &chapter); err != nil {
					return nil, fmt.Errorf("decode chapter %q after edit: %w", patch.Document.ID, err)
				}
				afterChapters = append(afterChapters, chapter)
			}
		}
		edits = append(edits, manuscriptEdits(beforeChapters, afterChapters)...)
	}
	if len(edits) > maxPreferenceEdits {
		edits = edits[len(edits)-maxPreferenceEdits:]
	}
	return edits, nil
}

func manuscriptEdits(before, after []domain.ManuscriptChapter) []domain.ManuscriptEditEvidence {
	type block struct {
		chapterID string
		text      string
	}
	beforeBlocks := make(map[string]block)
	afterBlocks := make(map[string]block)
	for _, chapter := range before {
		for _, current := range chapter.Blocks {
			beforeBlocks[chapter.ID+"\x00"+current.ID] = block{chapterID: chapter.ID, text: current.Text}
		}
	}
	for _, chapter := range after {
		for _, current := range chapter.Blocks {
			afterBlocks[chapter.ID+"\x00"+current.ID] = block{chapterID: chapter.ID, text: current.Text}
		}
	}
	keys := make([]string, 0, len(beforeBlocks)+len(afterBlocks))
	seen := make(map[string]struct{}, len(beforeBlocks)+len(afterBlocks))
	for key := range beforeBlocks {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range afterBlocks {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	edits := make([]domain.ManuscriptEditEvidence, 0)
	for _, key := range keys {
		old, hadOld := beforeBlocks[key]
		current, hasCurrent := afterBlocks[key]
		if hadOld && hasCurrent && old.text == current.text {
			continue
		}
		chapterID := old.chapterID
		if chapterID == "" {
			chapterID = current.chapterID
		}
		separator := strings.IndexByte(key, 0)
		edits = append(edits, domain.ManuscriptEditEvidence{
			ChapterID: chapterID, BlockID: key[separator+1:], Before: old.text, After: current.text,
		})
	}
	return edits
}

func (s *Service) InstallPackDirectory(
	ctx context.Context,
	path, changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	loaded, err := packloader.LoadDirectory(path)
	if err != nil {
		return InstalledPack{}, err
	}
	return s.installPack(ctx, loaded, changeID, userID, reason, createdAt)
}

func (s *Service) InstallPackArchive(
	ctx context.Context,
	path, changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	loaded, err := packloader.LoadArchive(path)
	if err != nil {
		return InstalledPack{}, err
	}
	return s.installPack(ctx, loaded, changeID, userID, reason, createdAt)
}

func (s *Service) InstallPackURL(
	ctx context.Context,
	url, changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	loaded, err := packloader.LoadURL(ctx, url)
	if err != nil {
		return InstalledPack{}, err
	}
	return s.installPack(ctx, loaded, changeID, userID, reason, createdAt)
}

func (s *Service) ExportPack(ctx context.Context, ref PackRef, path string) error {
	pack, err := loadPack(ctx, s.store, ref)
	if err != nil {
		return err
	}
	return packloader.ExportArchive(path, pack.Manifest)
}

func (s *Service) installPack(
	ctx context.Context,
	loaded packloader.Loaded,
	changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityPack, ID: loaded.Manifest.ID}
	base, err := currentRevision(ctx, s.store, target)
	if err != nil {
		return InstalledPack{}, err
	}
	content, err := json.Marshal(loaded.Manifest)
	if err != nil {
		return InstalledPack{}, fmt.Errorf("encode pack manifest: %w", err)
	}
	proposal := domain.Proposal{
		ID: changeID, Target: target, BaseRevision: base,
		Author: domain.Author{Kind: domain.AuthorUser, ID: userID}, Reason: reason,
		Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentPack, ID: loaded.Manifest.ID},
			Operation: domain.PatchPut, Content: content,
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: createdAt,
	}
	committed, err := s.commitUserProposal(ctx, proposal, createdAt)
	if err != nil {
		return InstalledPack{}, err
	}
	return InstalledPack{
		Manifest: loaded.Manifest, Revision: committed.NewRevision,
		Digest: loaded.Digest, Source: loaded.Root,
	}, nil
}

func (s *Service) SaveCreatorProfile(
	ctx context.Context,
	changeID, userID, reason string,
	profile domain.CreatorProfile,
	createdAt time.Time,
) (domain.ChangeSet, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProfile, ID: profile.ID, Scope: profile.Scope}
	base, err := currentRevision(ctx, s.store, target)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	content, err := json.Marshal(profile)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("encode creator profile: %w", err)
	}
	proposal := domain.Proposal{
		ID: changeID, Target: target, BaseRevision: base,
		Author: domain.Author{Kind: domain.AuthorUser, ID: userID}, Reason: reason,
		Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentCreatorProfile, ID: profile.ID},
			Operation: domain.PatchPut, Content: content,
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: createdAt,
	}
	return s.commitUserProposal(ctx, proposal, createdAt)
}

func (s *Service) CreatorProfile(
	ctx context.Context,
	id, scope string,
	revision domain.Revision,
) (domain.CreatorProfile, domain.Revision, error) {
	profile, err := loadCreatorProfile(ctx, s.store, CreatorProfileRef{ID: id, Scope: scope, Revision: revision})
	if err != nil {
		return domain.CreatorProfile{}, 0, err
	}
	return profile.Profile, profile.Revision, nil
}

func (s *Service) ConfirmPreferenceCandidate(
	ctx context.Context,
	changeID, userID, reason, profileID, scope, candidateID string,
	createdAt time.Time,
) (domain.ChangeSet, error) {
	record, recordErr := s.store.GetPreferenceCandidate(ctx, profileID, scope, candidateID)
	profile, _, err := s.CreatorProfile(ctx, profileID, scope, domain.InitialRevision)
	if errors.Is(err, store.ErrNotFound) {
		profile = domain.CreatorProfile{ID: profileID, Scope: scope}
	} else if err != nil {
		return domain.ChangeSet{}, err
	}
	var candidate domain.PreferenceCandidate
	derived := recordErr == nil
	if derived {
		candidate = record.Candidate
	} else if !errors.Is(recordErr, store.ErrNotFound) {
		return domain.ChangeSet{}, recordErr
	} else {
		index := -1
		for i, current := range profile.PreferenceCandidates {
			if current.ID == candidateID {
				index, candidate = i, current
				break
			}
		}
		if index < 0 {
			return domain.ChangeSet{}, fmt.Errorf("preference candidate %q: %w", candidateID, store.ErrNotFound)
		}
		profile.PreferenceCandidates = append(profile.PreferenceCandidates[:index], profile.PreferenceCandidates[index+1:]...)
	}
	existingRules := make(map[string]struct{}, len(profile.ExplicitRules))
	for _, rule := range profile.ExplicitRules {
		existingRules[rule] = struct{}{}
	}
	for _, rule := range candidate.ProposedRules {
		if _, exists := existingRules[rule]; !exists {
			profile.ExplicitRules = append(profile.ExplicitRules, rule)
			existingRules[rule] = struct{}{}
		}
	}
	if profile.Preferences == nil && len(candidate.ProposedPreferences) != 0 {
		profile.Preferences = make(map[string]string, len(candidate.ProposedPreferences))
	}
	for key, value := range candidate.ProposedPreferences {
		profile.Preferences[key] = value
	}
	changeSet, err := s.SaveCreatorProfile(ctx, changeID, userID, reason, profile, createdAt)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if derived {
		if err := s.store.ConfirmPreferenceCandidate(ctx, profileID, scope, candidateID, createdAt); err != nil {
			return domain.ChangeSet{}, err
		}
	}
	return changeSet, nil
}

func loadPack(ctx context.Context, authorityStore *store.Store, ref PackRef) (prompt.VersionedPack, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityPack, ID: ref.ID}
	revision := ref.Revision
	if revision == domain.InitialRevision {
		var err error
		revision, err = authorityStore.CurrentRevision(ctx, target)
		if err != nil {
			return prompt.VersionedPack{}, err
		}
	}
	document, err := authorityStore.GetDocument(
		ctx, target, domain.DocumentRef{Kind: domain.DocumentPack, ID: ref.ID}, revision,
	)
	if err != nil {
		return prompt.VersionedPack{}, err
	}
	var manifest domain.PackManifest
	if err := json.Unmarshal(document.Content, &manifest); err != nil {
		return prompt.VersionedPack{}, fmt.Errorf("decode pack %q: %w", ref.ID, err)
	}
	if err := manifest.Validate(); err != nil {
		return prompt.VersionedPack{}, err
	}
	return prompt.VersionedPack{Revision: revision, Manifest: manifest}, nil
}

func loadCreatorProfile(
	ctx context.Context,
	authorityStore *store.Store,
	ref CreatorProfileRef,
) (prompt.VersionedCreatorProfile, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProfile, ID: ref.ID, Scope: ref.Scope}
	revision := ref.Revision
	if revision == domain.InitialRevision {
		var err error
		revision, err = authorityStore.CurrentRevision(ctx, target)
		if err != nil {
			return prompt.VersionedCreatorProfile{}, err
		}
	}
	document, err := authorityStore.GetDocument(
		ctx, target, domain.DocumentRef{Kind: domain.DocumentCreatorProfile, ID: ref.ID}, revision,
	)
	if err != nil {
		return prompt.VersionedCreatorProfile{}, err
	}
	var profile domain.CreatorProfile
	if err := json.Unmarshal(document.Content, &profile); err != nil {
		return prompt.VersionedCreatorProfile{}, fmt.Errorf("decode creator profile %q/%q: %w", ref.ID, ref.Scope, err)
	}
	if err := profile.Validate(); err != nil {
		return prompt.VersionedCreatorProfile{}, err
	}
	return prompt.VersionedCreatorProfile{Revision: revision, Profile: profile}, nil
}

func currentRevision(
	ctx context.Context,
	authorityStore *store.Store,
	target domain.AuthorityTarget,
) (domain.Revision, error) {
	revision, err := authorityStore.CurrentRevision(ctx, target)
	if errors.Is(err, store.ErrNotFound) {
		return domain.InitialRevision, nil
	}
	return revision, err
}
