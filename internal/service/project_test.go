package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCreateProjectAndStartOperationFreezeExecutionProfile(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	project, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1",
		Reason: "按详细大纲创建作品", Draft: testProjectDraft(), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if project.Revision != 1 || len(project.Plan) != 4 || len(project.Ownership) != 2 {
		t.Fatalf("project = %#v", project)
	}
	repeated, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1",
		Reason: "按详细大纲创建作品", Draft: testProjectDraft(), CreatedAt: now.Add(time.Minute),
	})
	if err != nil || repeated.Revision != 1 {
		t.Fatalf("idempotent create = revision %d, %v", repeated.Revision, err)
	}

	first, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-1", ProjectID: project.ID,
		RunID: ensureServiceTestRun(t, ctx, authorityStore, project.ID, now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "deepseek-chat-config",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("start first operation: %v", err)
	}
	if first.Snapshot.ExecutionProfileDigest == "" {
		t.Fatal("operation did not freeze an execution profile")
	}

	profile := domain.CreatorProfile{ID: "user-1", Scope: "global", ExplicitRules: []string{"避免长段说教"}}
	if _, err := service.SaveCreatorProfile(ctx, "profile-1", "user-1", "保存全局偏好", profile, now.Add(90*time.Second)); err != nil {
		t.Fatalf("save creator profile: %v", err)
	}
	second, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-2", ProjectID: project.ID,
		RunID: ensureServiceTestRun(t, ctx, authorityStore, project.ID, now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CreatorProfiles:     []CreatorProfileRef{{ID: "user-1", Scope: "global"}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "deepseek-chat-config",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start second operation: %v", err)
	}
	if second.Snapshot.ExecutionProfileDigest == first.Snapshot.ExecutionProfileDigest {
		t.Fatal("new profile did not affect the new operation")
	}
	derived, err := service.DerivedDocuments(ctx, project.ID, project.Revision)
	if err != nil || len(derived) != 1 {
		t.Fatalf("derived context cache = %#v, %v", derived, err)
	}
	storedFirst, err := authorityStore.GetOperation(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first operation: %v", err)
	}
	if storedFirst.Snapshot.ExecutionProfileDigest != first.Snapshot.ExecutionProfileDigest {
		t.Fatal("later profile change mutated a running operation snapshot")
	}
	promptText, sources, err := service.Prompt(ctx, first.Snapshot.ExecutionProfileDigest)
	if err != nil {
		t.Fatalf("show prompt: %v", err)
	}
	if promptText == "" || len(sources) == 0 {
		t.Fatalf("prompt text or sources missing: %q %#v", promptText, sources)
	}
}

func TestLockedAIProposalRequiresServiceApproval(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	if _, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	fact, err := json.Marshal(domain.CanonFact{
		ID: "hero-bottom-line", Kind: domain.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line",
		PreviousValue: json.RawMessage(`"绝不滥杀"`), Value: json.RawMessage(`"绝不滥杀"`),
	})
	if err != nil {
		t.Fatalf("encode fact: %v", err)
	}
	proposal, err := service.Propose(ctx, domain.Proposal{
		ID: "change-bottom-line", Target: domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"},
		BaseRevision: 1, Author: domain.Author{Kind: domain.AuthorAI, ID: "writer.compose"},
		Reason: "剧情需要改变底线", Patches: []domain.Patch{{
			Document:  domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"},
			Operation: domain.PatchPut, Content: fact,
		}}, ApprovalState: domain.ApprovalPending, CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("prepare proposal: %v", err)
	}
	if proposal.ApprovalState != domain.ApprovalPending {
		t.Fatalf("proposal state = %s", proposal.ApprovalState)
	}
	committed, err := service.Approve(ctx, proposal.ID, "user-1", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("approve proposal: %v", err)
	}
	if committed.NewRevision != 2 {
		t.Fatalf("revision = %d, want 2", committed.NewRevision)
	}
}

func TestPackDirectoryUpdateOnlyAffectsNewOperations(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	if _, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	packDir := filepath.Join(t.TempDir(), "my-pack")
	if err := os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755); err != nil {
		t.Fatalf("create pack directory: %v", err)
	}
	manifest := `{
		// 用户直接编辑这个目录
		"id":"my-style","version":"1","name":"我的文风",
		"prompts":{"writer.chapter_draft":"prompts/chapter.md"},
	}`
	if err := os.WriteFile(filepath.Join(packDir, "pack.jsonc"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	promptPath := filepath.Join(packDir, "prompts", "chapter.md")
	if err := os.WriteFile(promptPath, []byte("多用短句"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	installed, err := service.InstallPackDirectory(ctx, packDir, "pack-v1", "user-1", "安装我的文风", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	first, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-with-pack-v1", ProjectID: "book-1",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-1", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), Packs: []PackRef{{ID: installed.Manifest.ID}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start first operation: %v", err)
	}
	firstPrompt, _, err := service.Prompt(ctx, first.Snapshot.ExecutionProfileDigest)
	if err != nil || !strings.Contains(firstPrompt, "多用短句") {
		t.Fatalf("first prompt = %q, %v", firstPrompt, err)
	}

	if err := os.WriteFile(promptPath, []byte("多用留白和潜台词"), 0o644); err != nil {
		t.Fatalf("update prompt: %v", err)
	}
	installed, err = service.InstallPackDirectory(ctx, packDir, "pack-v2", "user-1", "更新我的文风", now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("update pack: %v", err)
	}
	if installed.Revision != 2 {
		t.Fatalf("pack revision = %d, want 2", installed.Revision)
	}
	stillFrozen, _, err := service.Prompt(ctx, first.Snapshot.ExecutionProfileDigest)
	if err != nil || !strings.Contains(stillFrozen, "多用短句") || strings.Contains(stillFrozen, "潜台词") {
		t.Fatalf("old operation prompt changed: %q, %v", stillFrozen, err)
	}
	second, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-with-pack-v2", ProjectID: "book-1",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-1", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`), Packs: []PackRef{{ID: installed.Manifest.ID}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start second operation: %v", err)
	}
	secondPrompt, _, err := service.Prompt(ctx, second.Snapshot.ExecutionProfileDigest)
	if err != nil || !strings.Contains(secondPrompt, "多用留白和潜台词") {
		t.Fatalf("second prompt = %q, %v", secondPrompt, err)
	}
}

func TestProjectAssetsAndOverlayAutoAssembleIntoOperations(t *testing.T) {
	// D31 + §7.1：给这本书固定一套 Pack/Profile 与书级规则后，Operation 不再
	// 按命令传参，自动装配已启用资产；固定版本不随资产升级漂移，直到用户重新启用。
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	if _, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-assets", ChangeID: "create-book-assets", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	packDir := filepath.Join(t.TempDir(), "my-pack")
	if err := os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755); err != nil {
		t.Fatalf("create pack directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.jsonc"), []byte(`{
		"id":"my-style","version":"1","name":"我的文风",
		"prompts":{"writer.chapter_draft":"prompts/chapter.md"},
	}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	promptPath := filepath.Join(packDir, "prompts", "chapter.md")
	if err := os.WriteFile(promptPath, []byte("多用短句"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if _, err := service.InstallPackDirectory(ctx, packDir, "pack-v1", "user-1", "安装我的文风", now.Add(time.Minute)); err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := service.SaveCreatorProfile(ctx, "profile-assets", "user-1", "保存全局偏好", domain.CreatorProfile{
		ID: "user-1", Scope: "global", ExplicitRules: []string{"用动作与潜台词表达情绪"},
	}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("save creator profile: %v", err)
	}
	if _, err := service.SetProjectOverlay(ctx, SetProjectOverlayCommand{
		ProjectID: "book-assets", ChangeID: "overlay-1", UserID: "user-1",
		Rules: []string{"本书禁止旁白抒情"}, Reason: "确立书级规则", CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("set project overlay: %v", err)
	}
	if _, err := service.SetProjectAssets(ctx, SetProjectAssetsCommand{
		ProjectID: "book-assets", ChangeID: "assets-1", UserID: "user-1",
		Packs:           []PackRef{{ID: "my-style"}},
		CreatorProfiles: []CreatorProfileRef{{ID: "user-1", Scope: "global"}},
		Reason:          "固定本书资产", CreatedAt: now.Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("set project assets: %v", err)
	}

	operation, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-auto-assemble", ProjectID: "book-assets",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-assets", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start operation without asset params: %v", err)
	}
	text, _, err := service.Prompt(ctx, operation.Snapshot.ExecutionProfileDigest)
	if err != nil {
		t.Fatalf("read compiled prompt: %v", err)
	}
	for _, want := range []string{"多用短句", "用动作与潜台词表达情绪", "本书禁止旁白抒情"} {
		if !strings.Contains(text, want) {
			t.Fatalf("auto-assembled prompt missing %q: %q", want, text)
		}
	}

	// 资产升级不影响已固定的引用：新 Operation 仍用启用时确认的版本（D31）。
	if err := os.WriteFile(promptPath, []byte("多用留白和潜台词"), 0o644); err != nil {
		t.Fatalf("update prompt: %v", err)
	}
	if _, err := service.InstallPackDirectory(ctx, packDir, "pack-v2", "user-1", "升级文风", now.Add(6*time.Minute)); err != nil {
		t.Fatalf("upgrade pack: %v", err)
	}
	pinned, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-after-upgrade", ProjectID: "book-assets",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-assets", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(7 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start pinned operation: %v", err)
	}
	pinnedPrompt, _, err := service.Prompt(ctx, pinned.Snapshot.ExecutionProfileDigest)
	if err != nil || !strings.Contains(pinnedPrompt, "多用短句") || strings.Contains(pinnedPrompt, "潜台词表达情绪的新版") {
		t.Fatalf("pinned prompt drifted: %q, %v", pinnedPrompt, err)
	}
	if strings.Contains(pinnedPrompt, "多用留白和潜台词") {
		t.Fatalf("pinned pack reference floated to the upgraded version: %q", pinnedPrompt)
	}
}

func TestConfirmedPreferenceCarriesToAnotherBook(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	for i, projectID := range []string{"book-1", "book-2"} {
		if _, err := service.CreateProject(ctx, CreateProjectCommand{
			ProjectID: projectID, ChangeID: "create-" + projectID, UserID: "user-1",
			Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("create %s: %v", projectID, err)
		}
	}
	profile := domain.CreatorProfile{
		ID: "user-1", Scope: "global",
		PreferenceCandidates: []domain.PreferenceCandidate{{
			ID: "prefer-restraint", Summary: "偏好克制表达", Evidence: []string{"用户删掉了直白解释"},
			ProposedRules:   []string{"用动作和潜台词表达情绪，避免直接解释"},
			SourceProjectID: "book-1", SourceRevision: 1,
		}},
	}
	if _, err := service.SaveCreatorProfile(ctx, "profile-candidate", "user-1", "保存偏好候选", profile, now.Add(time.Minute)); err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	before, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "book-2-before-confirm", ProjectID: "book-2",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-2", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CreatorProfiles:     []CreatorProfileRef{{ID: "user-1", Scope: "global"}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start before confirmation: %v", err)
	}
	beforePrompt, _, err := service.Prompt(ctx, before.Snapshot.ExecutionProfileDigest)
	if err != nil {
		t.Fatalf("show before prompt: %v", err)
	}
	if strings.Contains(beforePrompt, "用动作和潜台词表达情绪") {
		t.Fatal("unconfirmed candidate affected another book")
	}
	if _, err := service.ConfirmPreferenceCandidate(
		ctx, "profile-confirm", "user-1", "确认全局偏好",
		"user-1", "global", "prefer-restraint", now.Add(3*time.Minute),
	); err != nil {
		t.Fatalf("confirm preference: %v", err)
	}
	after, err := service.StartOperation(ctx, StartOperationCommand{
		OperationID: "book-2-after-confirm", ProjectID: "book-2",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-2", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CreatorProfiles:     []CreatorProfileRef{{ID: "user-1", Scope: "global"}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("start after confirmation: %v", err)
	}
	afterPrompt, _, err := service.Prompt(ctx, after.Snapshot.ExecutionProfileDigest)
	if err != nil || !strings.Contains(afterPrompt, "用动作和潜台词表达情绪") {
		t.Fatalf("confirmed preference missing from second book: %q, %v", afterPrompt, err)
	}
}

func TestLearnsPreferenceCandidateFromUserManuscriptEditsBeforeConfirmation(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := NewWithExecutor(authorityStore, preferenceTestExecutor{})
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-learning", ChangeID: "create-book-learning", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	projection, err := api.ExportProject(ctx, "book-learning", 1)
	if err != nil {
		t.Fatalf("export revision 1: %v", err)
	}
	projection.Manuscript = []domain.ManuscriptChapter{{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "山门",
		Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "他感到非常悲伤，于是哭了。"}},
	}}
	proposal, err := api.ImportProject(ctx, "add-manuscript", "user-1", "加入正文", projection, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("import revision 2: %v", err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("approve revision 2: %v", err)
	}
	projection, err = api.ExportProject(ctx, "book-learning", 2)
	if err != nil {
		t.Fatalf("export revision 2: %v", err)
	}
	projection.Manuscript[0].Blocks[0].Text = "他捏皱袖口，半晌没有抬头。"
	proposal, err = api.ImportProject(ctx, "edit-manuscript", "user-1", "改为克制表达", projection, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("import revision 3: %v", err)
	}
	if _, err := api.Approve(ctx, proposal.ID, "user-1", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("approve revision 3: %v", err)
	}
	record, err := api.LearnPreference(ctx, LearnPreferenceCommand{
		CandidateID: "restraint", ProfileID: "user-1", Scope: "global", ProjectID: "book-learning",
		FromRevision: 2, ToRevision: 3, CreatedAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("learn preference: %v", err)
	}
	if record.State != "pending" || len(record.Candidate.Evidence) == 0 {
		t.Fatalf("candidate = %#v", record)
	}
	if _, _, err := api.CreatorProfile(ctx, "user-1", "global", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unconfirmed candidate became active: %v", err)
	}
	if _, err := api.ConfirmPreferenceCandidate(
		ctx, "confirm-learned", "user-1", "确认克制表达", "user-1", "global", "restraint", now.Add(6*time.Minute),
	); err != nil {
		t.Fatalf("confirm learned preference: %v", err)
	}
	profile, _, err := api.CreatorProfile(ctx, "user-1", "global", 0)
	if err != nil {
		t.Fatalf("load confirmed profile: %v", err)
	}
	if !slices.Contains(profile.ExplicitRules, "用动作代替直接情绪解释") {
		t.Fatalf("confirmed profile = %#v", profile)
	}
	pending, err := api.PreferenceCandidates(ctx, "user-1", "global")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending candidates = %#v, %v", pending, err)
	}
}

type preferenceTestExecutor struct{}

func (preferenceTestExecutor) ModelConfigDigest() string { return "model" }

func (preferenceTestExecutor) Execute(context.Context, domain.Operation, prompt.Compiled) (domain.OperationOutcome, error) {
	return domain.OperationOutcome{}, errors.New("story execution is not used in preference test")
}

func (preferenceTestExecutor) AnalyzePreference(
	_ context.Context,
	input domain.PreferenceLearningInput,
) (domain.PreferenceCandidate, error) {
	if len(input.ManuscriptEdits) != 1 || input.ManuscriptEdits[0].Before == input.ManuscriptEdits[0].After {
		return domain.PreferenceCandidate{}, errors.New("missing manuscript edit evidence")
	}
	return domain.PreferenceCandidate{
		Summary: "偏好克制地表现情绪", Evidence: []string{"将直接情绪解释改成动作细节"},
		ProposedRules: []string{"用动作代替直接情绪解释"},
	}, nil
}

func (preferenceTestExecutor) Analyze(
	_ context.Context,
	_ domain.Proposal,
	_ change.StructuralImpact,
) (json.RawMessage, error) {
	report := change.SemanticImpactReport{
		Status: change.SemanticImpactConflict,
		Findings: []change.SemanticImpactFinding{{
			Document:    &domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"},
			Explanation: "第一章已经用行动落实了旧底线",
		}},
		Options: []change.ResolutionOption{
			{Strategy: change.ResolutionRewriteAffected, ChapterIDs: []string{"chapter-1"}, Explanation: "同步重写第一章"},
			{Strategy: change.ResolutionReinterpretFuture, Explanation: "保留旧章并在后文解释变化"},
			{Strategy: change.ResolutionAbandon, Explanation: "放弃本次事实变更"},
		},
	}
	return json.Marshal(report)
}

func TestSemanticConflictResolutionCreatesOneAtomicAffectedRewrite(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := NewWithExecutor(authorityStore, preferenceTestExecutor{})
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "semantic-book", ChangeID: "create-semantic-book", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	projection, err := api.ExportProject(ctx, "semantic-book", 0)
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	projection.Manuscript = append(projection.Manuscript, domain.ManuscriptChapter{
		ID: "chapter-1", PlanNodeID: "chapter-plan-1", Number: 1, Title: "第一章",
		Blocks: []domain.ManuscriptBlock{{ID: "block-1", Text: "他在山门前放下了刀。"}},
	})
	manuscriptProposal, err := api.ImportProject(
		ctx, "add-semantic-manuscript", "user-1", "写入既有第一章", projection, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("prepare manuscript: %v", err)
	}
	if _, err := api.Approve(ctx, manuscriptProposal.ID, "user-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("approve manuscript: %v", err)
	}

	projection, err = api.ExportProject(ctx, "semantic-book", 0)
	if err != nil {
		t.Fatalf("export changed project: %v", err)
	}
	projection.Canon[0].Value = json.RawMessage(`"可以为目的伤害无辜"`)
	proposal, err := api.ImportProjectWithSemantic(
		ctx, "change-bottom-line", "user-1", "改变角色底线", projection, now.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatalf("prepare semantic change: %v", err)
	}
	var report change.SemanticImpactReport
	if err := json.Unmarshal(proposal.Impact.Semantic, &report); err != nil || report.Status != change.SemanticImpactConflict {
		t.Fatalf("semantic impact = %#v, %v", report, err)
	}
	resolved, err := api.ResolveProposal(ctx, ResolveProposalCommand{
		ProposalID: proposal.ID, UserID: "user-1", Strategy: string(change.ResolutionRewriteAffected),
		RunID:     ensureServiceTestRun(t, ctx, authorityStore, "semantic-book", now),
		CreatedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("resolve semantic conflict: %v", err)
	}
	if resolved.ChangeSet == nil || resolved.ChangeSet.NewRevision != 3 || len(resolved.Operations) != 1 {
		t.Fatalf("resolution = %#v", resolved)
	}
	operation := resolved.Operations[0]
	if operation.Kind != domain.OperationRewriteAffected || operation.Snapshot.BaseRevision != 3 || operation.State != domain.OperationQueued {
		t.Fatalf("rewrite operation = %#v", operation)
	}
}

func TestEditableProjectProjectionProducesVersionedProposal(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	if _, err := service.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	projection, err := service.ExportProject(ctx, "book-1", 0)
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	for i := range projection.Plan {
		if projection.Plan[i].ID == "chapter-plan-1" {
			projection.Plan[i].Summary = "主角在暴雨中抵达山门"
		}
	}
	projection.Canon[0].Value = json.RawMessage(`"绝不主动伤害无辜"`)
	proposal, err := service.ImportProject(
		ctx, "projection-edit", "user-1", "调整第一章大纲和角色底线", projection, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("import project: %v", err)
	}
	if len(proposal.Patches) != 2 || len(proposal.Impact.Structural) == 0 {
		t.Fatalf("proposal = %#v", proposal)
	}
	if _, err := service.Approve(ctx, proposal.ID, "user-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("approve imported projection: %v", err)
	}
	updated, err := service.Project(ctx, "book-1", 0)
	if err != nil {
		t.Fatalf("read updated project: %v", err)
	}
	var chapterSummary string
	for _, node := range updated.Plan {
		if node.ID == "chapter-plan-1" {
			chapterSummary = node.Summary
		}
	}
	if updated.Revision != 2 || chapterSummary != "主角在暴雨中抵达山门" {
		t.Fatalf("updated project = %#v", updated)
	}
	if _, err := service.ImportProject(
		ctx, "stale-projection", "user-1", "重复导入旧基线", projection, now.Add(3*time.Minute),
	); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale import error = %v, want ErrRevisionConflict", err)
	}
}

func TestOperationLifecycleKeepsExplicitState(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := New(authorityStore)
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-lifecycle", ChangeID: "create-book-lifecycle", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	started, err := api.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-lifecycle", ProjectID: "book-lifecycle",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-lifecycle", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	paused, err := api.PauseOperation(ctx, started.ID, now.Add(2*time.Minute))
	if err != nil || paused.State != domain.OperationPaused {
		t.Fatalf("pause = %s, %v", paused.State, err)
	}
	reprioritized, err := api.ReprioritizeOperation(ctx, started.ID, 42, now.Add(3*time.Minute))
	if err != nil || reprioritized.Priority != 42 {
		t.Fatalf("reprioritize = %d, %v", reprioritized.Priority, err)
	}
	resumed, err := api.ResumeOperation(ctx, started.ID, now.Add(4*time.Minute))
	if err != nil || resumed.State != domain.OperationQueued {
		t.Fatalf("resume = %s, %v", resumed.State, err)
	}
	cancelled, err := api.CancelOperation(ctx, started.ID, now.Add(5*time.Minute))
	if err != nil || cancelled.State != domain.OperationCancelled || !strings.Contains(cancelled.Error, "workspace retained") {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
}

func TestRestartOperationFreezesNewSnapshotAndSeedsWorkspace(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := New(authorityStore)
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-restart", ChangeID: "create-book-restart", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	started, err := api.StartOperation(ctx, StartOperationCommand{
		OperationID: "write-before-restart", ProjectID: "book-restart",
		RunID: ensureServiceTestRun(t, ctx, authorityStore, "book-restart", now),
		Kind:  domain.OperationWriteChapter, WorkerProfileID: "writer.compose",
		Input:               json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	running, err := authorityStore.ClaimOperationForModel(
		ctx, started.ID, "worker-1", "model", time.Minute, now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	if _, err := authorityStore.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
		OperationID: running.ID, Key: "chapter/chapter-1", MediaType: "text/plain",
		Content: []byte("保留的工作稿"), UpdatedAt: now.Add(3 * time.Minute),
	}, 0, running.Attempt); err != nil {
		t.Fatalf("write workspace: %v", err)
	}
	if _, err := authorityStore.TransitionOperation(
		ctx, running.ID, domain.OperationRunning, domain.OperationFailed, "provider failed", now.Add(4*time.Minute),
	); err != nil {
		t.Fatalf("fail operation: %v", err)
	}
	restarted, err := api.RestartOperation(ctx, RestartOperationCommand{
		FromOperationID: running.ID, OperationID: "write-after-restart", CreatedAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("restart operation: %v", err)
	}
	if restarted.State != domain.OperationQueued || restarted.Snapshot != started.Snapshot {
		t.Fatalf("restarted = %#v", restarted)
	}
	artifacts, err := authorityStore.ListWorkspaceArtifacts(ctx, restarted.ID)
	if err != nil {
		t.Fatalf("list seeded workspace: %v", err)
	}
	if len(artifacts) != 1 || string(artifacts[0].Content) != "保留的工作稿" {
		t.Fatalf("seeded artifacts = %#v", artifacts)
	}
	if _, err := api.RestartOperation(ctx, RestartOperationCommand{
		FromOperationID: running.ID, OperationID: restarted.ID, CreatedAt: now.Add(6 * time.Minute),
	}); err != nil {
		t.Fatalf("idempotent restart: %v", err)
	}
	fresh := json.RawMessage(`{"chapter_plan_id":"chapter-plan-1","directives":[{"id":"d-1"}]}`)
	if _, err := api.RestartOperation(ctx, RestartOperationCommand{
		FromOperationID: running.ID, OperationID: restarted.ID, Input: fresh, CreatedAt: now.Add(6 * time.Minute),
	}); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("restart with different input must conflict, got %v", err)
	}
	overridden, err := api.RestartOperation(ctx, RestartOperationCommand{
		FromOperationID: running.ID, OperationID: "write-after-restart-2", Input: fresh, CreatedAt: now.Add(7 * time.Minute),
	})
	if err != nil {
		t.Fatalf("restart with fresh input: %v", err)
	}
	if string(overridden.Input) != string(fresh) {
		t.Fatalf("successor input = %s, want %s", overridden.Input, fresh)
	}
}

func TestReloadPromptAppliesCreatorProfileWithoutCreatingOperation(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	api := New(authorityStore)
	now := serviceTime()
	if _, err := api.CreateProject(ctx, CreateProjectCommand{
		ProjectID: "book-prompt", ChangeID: "create-book-prompt", UserID: "user-1",
		Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := api.SaveCreatorProfile(ctx, "profile-prompt", "user-1", "保存文风", domain.CreatorProfile{
		ID: "user-1", Scope: "global", ExplicitRules: []string{"对白避免解释已知信息"},
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	result, err := api.ReloadPrompt(ctx, ReloadPromptCommand{
		ProjectID: "book-prompt", Kind: domain.OperationWriteChapter,
		WorkerProfileID: "writer.compose", Input: json.RawMessage(`{"chapter_plan_id":"chapter-plan-1"}`),
		CreatorProfiles:     []CreatorProfileRef{{ID: "user-1", Scope: "global"}},
		CoreProtocolVersion: "core-v1", ModelConfigDigest: "model",
		ApprovalPolicy: domain.ApprovalManual, CreatedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("reload prompt: %v", err)
	}
	text, _, err := api.Prompt(ctx, result.ExecutionProfileDigest)
	if err != nil || !strings.Contains(text, "对白避免解释已知信息") {
		t.Fatalf("reloaded prompt = %q, %v", text, err)
	}
	if _, err := api.Operation(ctx, "book-prompt"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("prompt reload created an operation: %v", err)
	}
}

func testProjectDraft() ProjectDraft {
	bottomLine := domain.DocumentRef{Kind: domain.DocumentCanon, ID: "hero-bottom-line"}
	ending := domain.DocumentRef{Kind: domain.DocumentPlan, ID: "ending-beat"}
	return ProjectDraft{
		Intent: domain.Intent{Premise: "一个凡人进入修仙宗门", EndingDirection: "守住本心后归乡"},
		Plan: []domain.PlanNode{
			{ID: "volume-1", Kind: domain.PlanVolume, Title: "入道", Summary: "进入修行世界"},
			{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "通过山门考验"},
			{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达山门"},
			{ID: "ending-beat", Kind: domain.PlanBeat, ParentID: "chapter-plan-1", Title: "结局锚点", Summary: "守住本心"},
		},
		Canon: []domain.CanonFact{{
			ID: bottomLine.ID, Kind: domain.CanonWorldRule, SubjectID: "hero", Predicate: "rule.bottom_line", Value: json.RawMessage(`"绝不滥杀"`),
		}},
		Ownership: []domain.OwnershipRule{
			{Target: bottomLine, Control: domain.ControlLocked},
			{Target: ending, Control: domain.ControlLocked},
		},
	}
}

func openServiceStore(t *testing.T) *store.Store {
	t.Helper()
	authorityStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := authorityStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return authorityStore
}

func ensureServiceTestRun(
	t *testing.T,
	ctx context.Context,
	authorityStore *store.Store,
	projectID string,
	now time.Time,
) string {
	t.Helper()
	if active, err := authorityStore.ActiveCreationRun(ctx, projectID); err == nil {
		return active.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read service test run: %v", err)
	}
	run := domain.CreationRun{
		ID: "run:" + projectID, ProjectID: projectID,
		Goal: domain.CreationRunGoal{Premise: "测试创作", TargetChapters: 3},
		Strategy: domain.CreationRunStrategy{
			PlanWindowChapters: 3, ReviewCadence: domain.ReviewPerPlanWindow, AutoRepairBudget: 3,
		},
		Preset: domain.CreationRunPreset{
			Source: "test", Digest: "test-preset", Approval: domain.ApprovalAuto,
		},
		State: domain.RunRunning, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := authorityStore.CreateCreationRun(ctx, run); err != nil {
		t.Fatalf("create service test run: %v", err)
	}
	return run.ID
}

func serviceTime() time.Time {
	return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
}

func TestListProjectsReturnsLibrary(t *testing.T) {
	ctx := context.Background()
	authorityStore := openServiceStore(t)
	service := New(authorityStore)
	now := serviceTime()
	if library, err := service.ListProjects(ctx); err != nil || len(library) != 0 {
		t.Fatalf("empty library = %#v, %v", library, err)
	}
	for _, id := range []string{"book-b", "book-a"} {
		if _, err := service.CreateProject(ctx, CreateProjectCommand{
			ProjectID: id, ChangeID: "create-" + id, UserID: "user-1",
			Reason: "创建作品", Draft: testProjectDraft(), CreatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	library, err := service.ListProjects(ctx)
	if err != nil || len(library) != 2 {
		t.Fatalf("library = %#v, %v", library, err)
	}
	if library[0].ID != "book-a" || library[1].ID != "book-b" || library[0].Revision != 1 {
		t.Fatalf("library order = %#v", library)
	}
}
