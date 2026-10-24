package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	packloader "github.com/voocel/ainovel-cli/internal/infra/capability/pack"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Catalog manages reusable creative packs and creator preferences.
type Catalog struct {
	store    *store.Store
	changes  *change.Engine
	projects *projectdoc.Repository
	official *Official
	analyzer PreferenceAnalyzer
}

func New(
	authorityStore *store.Store,
	changes *change.Engine,
	projects *projectdoc.Repository,
	official *Official,
	analyzer PreferenceAnalyzer,
) *Catalog {
	return &Catalog{store: authorityStore, changes: changes, projects: projects, official: official, analyzer: analyzer}
}

type PackRef struct {
	ID       string         `json:"id"`
	Revision model.Revision `json:"revision,omitempty"`
}

type CreatorProfileRef struct {
	ID       string         `json:"id"`
	Scope    string         `json:"scope"`
	Revision model.Revision `json:"revision,omitempty"`
}

type InstalledPack struct {
	Manifest model.PackManifest `json:"manifest"`
	Revision model.Revision     `json:"revision"`
	Digest   string             `json:"digest"`
	Source   string             `json:"source"`
}

func (s *Catalog) EvaluatePack(ctx context.Context, ref PackRef, output string) (packloader.EvalResult, error) {
	pack, err := LoadPack(ctx, s.store, ref)
	if err != nil {
		return packloader.EvalResult{}, err
	}
	return packloader.RunEvals(pack.Manifest, output)
}

type PreferenceAnalyzer interface {
	AnalyzePreference(context.Context, model.PreferenceLearningInput) (model.PreferenceCandidate, error)
}

type LearnPreferenceCommand struct {
	CandidateID  string
	ProfileID    string
	Scope        string
	ProjectID    string
	FromRevision model.Revision
	ToRevision   model.Revision
	CreatedAt    time.Time
}

func (s *Catalog) LearnPreference(ctx context.Context, command LearnPreferenceCommand) (model.PreferenceCandidateRecord, error) {
	if s.analyzer == nil {
		return model.PreferenceCandidateRecord{}, fmt.Errorf("configured runtime does not provide preference analysis: %w", model.ErrInvalid)
	}
	before, err := s.projects.Project(ctx, command.ProjectID, command.FromRevision)
	if err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	after, err := s.projects.Project(ctx, command.ProjectID, command.ToRevision)
	if err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	edits, err := s.userManuscriptEdits(ctx, command.ProjectID, before.Revision, after.Revision)
	if err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	if len(edits) == 0 {
		return model.PreferenceCandidateRecord{}, fmt.Errorf(
			"revisions %d..%d contain no direct user manuscript edits to learn from: %w",
			before.Revision, after.Revision, model.ErrInvalid,
		)
	}
	input := model.PreferenceLearningInput{
		CandidateID: command.CandidateID, ProfileID: command.ProfileID, Scope: command.Scope,
		ProjectID: command.ProjectID, FromRevision: before.Revision, ToRevision: after.Revision,
		ManuscriptEdits: edits,
	}
	if err := input.Validate(); err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	candidate, err := s.analyzer.AnalyzePreference(ctx, input)
	if err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	candidate.ID = input.CandidateID
	candidate.SourceProjectID = input.ProjectID
	candidate.SourceRevision = input.ToRevision
	if err := candidate.Validate(); err != nil {
		return model.PreferenceCandidateRecord{}, err
	}
	return s.store.SavePreferenceCandidate(ctx, command.ProfileID, command.Scope, candidate, command.CreatedAt)
}

func (s *Catalog) PreferenceCandidates(ctx context.Context, profileID, scope string) ([]model.PreferenceCandidateRecord, error) {
	return s.store.ListPreferenceCandidates(ctx, profileID, scope)
}

// 学习证据上限：超限时按“最近优先”截断，避免长区间把上下文打爆。
const maxPreferenceEdits = 200

// userManuscriptEdits 逐个 Revision 走 ChangeSet，只收集用户直接修改（作者为
// user 且不归属任何 Operation）产生的正文编辑；AI 提交一律排除，防止把 AI 自己
// 的文风学成“用户偏好”形成自我强化回路。
func (s *Catalog) userManuscriptEdits(
	ctx context.Context,
	projectID string,
	from, to model.Revision,
) ([]model.ManuscriptEditEvidence, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID}
	edits := make([]model.ManuscriptEditEvidence, 0)
	for revision := from + 1; revision <= to; revision++ {
		changeSet, err := s.store.GetChangeSetByRevision(ctx, target, revision)
		if err != nil {
			return nil, err
		}
		if changeSet.Author.Kind != model.AuthorUser || changeSet.OperationID != "" {
			continue
		}
		var beforeChapters, afterChapters []model.ManuscriptChapter
		for _, patch := range changeSet.Patches {
			if patch.Document.Kind != model.DocumentManuscript {
				continue
			}
			if changeSet.BaseRevision > model.InitialRevision {
				previous, err := s.store.GetDocument(ctx, target, patch.Document, changeSet.BaseRevision)
				if err == nil {
					var chapter model.ManuscriptChapter
					if err := json.Unmarshal(previous.Content, &chapter); err != nil {
						return nil, fmt.Errorf("decode chapter %q before edit: %w", patch.Document.ID, err)
					}
					beforeChapters = append(beforeChapters, chapter)
				} else if !errors.Is(err, model.ErrNotFound) {
					return nil, err
				}
			}
			if patch.Operation == model.PatchPut {
				var chapter model.ManuscriptChapter
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

func manuscriptEdits(before, after []model.ManuscriptChapter) []model.ManuscriptEditEvidence {
	type block struct {
		chapterID string
		chapter   int
		text      string
	}
	beforeBlocks := make(map[string]block)
	afterBlocks := make(map[string]block)
	for _, chapter := range before {
		for _, current := range chapter.Blocks {
			beforeBlocks[chapter.ID+"\x00"+current.ID] = block{chapterID: chapter.ID, chapter: chapter.Number, text: current.Text}
		}
	}
	for _, chapter := range after {
		for _, current := range chapter.Blocks {
			afterBlocks[chapter.ID+"\x00"+current.ID] = block{chapterID: chapter.ID, chapter: chapter.Number, text: current.Text}
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
	edits := make([]model.ManuscriptEditEvidence, 0)
	for _, key := range keys {
		old, hadOld := beforeBlocks[key]
		current, hasCurrent := afterBlocks[key]
		if hadOld && hasCurrent && old.text == current.text {
			continue
		}
		chapter := old
		if !hadOld {
			chapter = current
		}
		separator := strings.IndexByte(key, 0)
		edits = append(edits, model.ManuscriptEditEvidence{
			ChapterID: chapter.chapterID, Chapter: chapter.chapter, BlockID: key[separator+1:], Before: old.text, After: current.text,
		})
	}
	return edits
}

func (s *Catalog) InstallPackDirectory(
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

func (s *Catalog) InstallPackArchive(
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

func (s *Catalog) InstallPackURL(
	ctx context.Context,
	url, expectedSHA256, changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	loaded, err := packloader.LoadURL(ctx, url, expectedSHA256)
	if err != nil {
		return InstalledPack{}, err
	}
	return s.installPack(ctx, loaded, changeID, userID, reason, createdAt)
}

func (s *Catalog) ExportPack(ctx context.Context, ref PackRef, path string) error {
	pack, err := LoadPack(ctx, s.store, ref)
	if err != nil {
		return err
	}
	return packloader.ExportArchive(path, pack.Manifest)
}

func (s *Catalog) installPack(
	ctx context.Context,
	loaded packloader.Loaded,
	changeID, userID, reason string,
	createdAt time.Time,
) (InstalledPack, error) {
	if loaded.Manifest.ID == OfficialPackID {
		return InstalledPack{}, fmt.Errorf("pack id %q is reserved for the built-in official pack; change the id to install a copy: %w", OfficialPackID, model.ErrInvalid)
	}
	if err := validateSlots(loaded.Manifest); err != nil {
		return InstalledPack{}, err
	}
	target := model.AuthorityTarget{Kind: model.AuthorityPack, ID: loaded.Manifest.ID}
	base, err := currentRevision(ctx, s.store, target)
	if err != nil {
		return InstalledPack{}, err
	}
	content, err := json.Marshal(loaded.Manifest)
	if err != nil {
		return InstalledPack{}, fmt.Errorf("encode pack manifest: %w", err)
	}
	committed, err := commitPack(ctx, s.changes, target, base, content, changeID, userID, reason, createdAt)
	if err != nil {
		return InstalledPack{}, err
	}
	return InstalledPack{
		Manifest: loaded.Manifest, Revision: committed.NewRevision,
		Digest: loaded.Digest, Source: loaded.Root,
	}, nil
}

// validateSlots 拒绝未知的 Prompt 槽位：写错的槽位不会装配进任何任务，装上了也不生效（S6）。
func validateSlots(manifest model.PackManifest) error {
	for slot := range manifest.PromptOverlays {
		if !slices.Contains(prompt.Slots, prompt.Slot(slot)) {
			known := make([]string, 0, len(prompt.Slots))
			for _, slot := range prompt.Slots {
				known = append(known, string(slot))
			}
			return fmt.Errorf("unknown prompt slot %q, known slots: %s: %w", slot, strings.Join(known, ", "), model.ErrInvalid)
		}
	}
	return nil
}

// commitPack 以用户身份把 Pack 清单提交为该 Pack 权威流的新版本。
func commitPack(
	ctx context.Context,
	changes *change.Engine,
	target model.AuthorityTarget,
	base model.Revision,
	content json.RawMessage,
	changeID, userID, reason string,
	at time.Time,
) (model.ChangeSet, error) {
	return changes.CommitUser(ctx, model.Proposal{
		ID: changeID, Target: target, BaseRevision: base,
		Author: model.Author{Kind: model.AuthorUser, ID: userID}, Reason: reason,
		Patches: []model.Patch{{
			Document:  model.DocumentRef{Kind: model.DocumentPack, ID: target.ID},
			Operation: model.PatchPut, Content: content,
		}}, ApprovalState: model.ApprovalPending, CreatedAt: at,
	})
}

func (s *Catalog) SaveCreatorProfile(
	ctx context.Context,
	changeID, userID, reason string,
	profile model.CreatorProfile,
	createdAt time.Time,
) (model.ChangeSet, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProfile, ID: profile.ID, Scope: profile.Scope}
	base, err := currentRevision(ctx, s.store, target)
	if err != nil {
		return model.ChangeSet{}, err
	}
	content, err := json.Marshal(profile)
	if err != nil {
		return model.ChangeSet{}, fmt.Errorf("encode creator profile: %w", err)
	}
	proposal := model.Proposal{
		ID: changeID, Target: target, BaseRevision: base,
		Author: model.Author{Kind: model.AuthorUser, ID: userID}, Reason: reason,
		Patches: []model.Patch{{
			Document:  model.DocumentRef{Kind: model.DocumentCreatorProfile, ID: profile.ID},
			Operation: model.PatchPut, Content: content,
		}}, ApprovalState: model.ApprovalPending, CreatedAt: createdAt,
	}
	return s.changes.CommitUser(ctx, proposal)
}

func (s *Catalog) CreatorProfile(
	ctx context.Context,
	id, scope string,
	revision model.Revision,
) (model.CreatorProfile, model.Revision, error) {
	profile, err := LoadCreatorProfile(ctx, s.store, CreatorProfileRef{ID: id, Scope: scope, Revision: revision})
	if err != nil {
		return model.CreatorProfile{}, 0, err
	}
	return profile.Profile, profile.Revision, nil
}

func (s *Catalog) ConfirmPreferenceCandidate(
	ctx context.Context,
	changeID, userID, reason, profileID, scope, candidateID string,
	createdAt time.Time,
) (model.ChangeSet, error) {
	record, recordErr := s.store.GetPreferenceCandidate(ctx, profileID, scope, candidateID)
	profile, _, err := s.CreatorProfile(ctx, profileID, scope, model.InitialRevision)
	if errors.Is(err, model.ErrNotFound) {
		profile = model.CreatorProfile{ID: profileID, Scope: scope}
	} else if err != nil {
		return model.ChangeSet{}, err
	}
	var candidate model.PreferenceCandidate
	derived := recordErr == nil
	if derived {
		candidate = record.Candidate
	} else if !errors.Is(recordErr, model.ErrNotFound) {
		return model.ChangeSet{}, recordErr
	} else {
		index := -1
		for i, current := range profile.PreferenceCandidates {
			if current.ID == candidateID {
				index, candidate = i, current
				break
			}
		}
		if index < 0 {
			return model.ChangeSet{}, fmt.Errorf("preference candidate %q: %w", candidateID, model.ErrNotFound)
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
		return model.ChangeSet{}, err
	}
	if derived {
		if err := s.store.ConfirmPreferenceCandidate(ctx, profileID, scope, candidateID, createdAt); err != nil {
			return model.ChangeSet{}, err
		}
	}
	return changeSet, nil
}

func LoadPack(ctx context.Context, authorityStore *store.Store, ref PackRef) (prompt.VersionedPack, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityPack, ID: ref.ID}
	revision := ref.Revision
	if revision == model.InitialRevision {
		var err error
		revision, err = authorityStore.CurrentRevision(ctx, target)
		if err != nil {
			return prompt.VersionedPack{}, err
		}
	}
	document, err := authorityStore.GetDocument(
		ctx, target, model.DocumentRef{Kind: model.DocumentPack, ID: ref.ID}, revision,
	)
	if err != nil {
		return prompt.VersionedPack{}, err
	}
	var manifest model.PackManifest
	if err := json.Unmarshal(document.Content, &manifest); err != nil {
		return prompt.VersionedPack{}, fmt.Errorf("decode pack %q: %w", ref.ID, err)
	}
	if err := manifest.Validate(); err != nil {
		return prompt.VersionedPack{}, err
	}
	return prompt.VersionedPack{Revision: revision, Manifest: manifest}, nil
}

func LoadCreatorProfile(
	ctx context.Context,
	authorityStore *store.Store,
	ref CreatorProfileRef,
) (prompt.VersionedCreatorProfile, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProfile, ID: ref.ID, Scope: ref.Scope}
	revision := ref.Revision
	if revision == model.InitialRevision {
		var err error
		revision, err = authorityStore.CurrentRevision(ctx, target)
		if err != nil {
			return prompt.VersionedCreatorProfile{}, err
		}
	}
	document, err := authorityStore.GetDocument(
		ctx, target, model.DocumentRef{Kind: model.DocumentCreatorProfile, ID: ref.ID}, revision,
	)
	if err != nil {
		return prompt.VersionedCreatorProfile{}, err
	}
	var profile model.CreatorProfile
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
	target model.AuthorityTarget,
) (model.Revision, error) {
	revision, err := authorityStore.CurrentRevision(ctx, target)
	if errors.Is(err, model.ErrNotFound) {
		return model.InitialRevision, nil
	}
	return revision, err
}
