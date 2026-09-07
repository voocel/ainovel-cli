package headless

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tailscale/hujson"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/service"
)

func Run(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return writeHelp(stdout)
	}
	switch args[0] {
	case "quick":
		return runQuick(ctx, api, args[1:], stdout, stderr)
	case "creation":
		return runCreation(ctx, api, args[1:], stdout, stderr)
	case "project":
		return runProject(ctx, api, args[1:], stdout, stderr)
	case "proposal":
		return runProposal(ctx, api, args[1:], stdout, stderr)
	case "operation":
		return runOperation(ctx, api, args[1:], stdout, stderr)
	case "prompt":
		return runPrompt(ctx, api, args[1:], stdout, stderr)
	case "pack":
		return runPack(ctx, api, args[1:], stdout, stderr)
	case "profile":
		return runProfile(ctx, api, args[1:], stdout, stderr)
	case "help":
		return writeHelp(stdout)
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func runCreation(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("creation 需要 start、show、strategy、pause、cancel 或 events 子命令")
	}
	switch args[0] {
	case "start":
		flags := newFlags("creation start", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		projectID := flags.String("project", "", "Project ID")
		premise := flags.String("premise", "", "本轮创作目标")
		chapters := flags.Int("chapters", 0, "目标章节数")
		window := flags.Int("window", 3, "滚动规划窗口")
		repairBudget := flags.Int("repair-budget", -1, "允许自动修订次数；默认等于目标章节数")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" || *projectID == "" || *premise == "" || *chapters <= 0 || *window <= 0 || *repairBudget < -1 {
			return fmt.Errorf("creation start 需要 --id --project --premise、正数 --chapters/--window 和非负 --repair-budget")
		}
		if *repairBudget == -1 {
			*repairBudget = *chapters
		}
		project, err := api.Project(ctx, *projectID, domain.InitialRevision)
		if err != nil {
			return err
		}
		approval := project.Approval
		if approval == "" {
			approval = domain.ApprovalAuto
		}
		strategy := domain.CreationRunStrategy{
			PlanWindowChapters: *window,
			ReviewCadence:      domain.ReviewPerPlanWindow,
			AutoRepairBudget:   *repairBudget,
		}
		preset, err := domain.NewCreationRunPreset("custom", approval, strategy)
		if err != nil {
			return err
		}
		run, err := api.StartCreationRun(ctx, service.StartCreationRunCommand{
			RunID: *runID, ProjectID: *projectID,
			Goal:     domain.CreationRunGoal{Premise: *premise, TargetChapters: *chapters},
			Strategy: strategy, Preset: preset,
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, run, err)
	case "show":
		flags := newFlags("creation show", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" {
			return fmt.Errorf("creation show 需要 --id")
		}
		run, err := api.CreationRun(ctx, *runID)
		return writeResult(stdout, run, err)
	case "strategy":
		flags := newFlags("creation strategy", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		window := flags.Int("window", 0, "滚动规划窗口；0 表示保持当前值")
		repairBudget := flags.Int("repair-budget", -1, "自动修订预算；-1 表示保持当前值")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" || *window < 0 || *repairBudget < -1 ||
			(*window == 0 && *repairBudget == -1) || flags.NArg() != 0 {
			return fmt.Errorf("creation strategy 需要 --id 以及 --window 或 --repair-budget 至少之一")
		}
		run, err := api.CreationRun(ctx, *runID)
		if err != nil {
			return err
		}
		strategy := run.Strategy
		if *window > 0 {
			strategy.PlanWindowChapters = *window
		}
		if *repairBudget >= 0 {
			strategy.AutoRepairBudget = *repairBudget
		}
		updated, err := api.UpdateCreationRunStrategy(ctx, *runID, strategy, time.Now().UTC())
		return writeResult(stdout, updated, err)
	case "events":
		flags := newFlags("creation events", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" {
			return fmt.Errorf("creation events 需要 --id")
		}
		events, err := api.CreationRunEvents(ctx, *runID)
		return writeResult(stdout, events, err)
	case "pause", "cancel":
		flags := newFlags("creation "+args[0], stderr)
		runID := flags.String("id", "", "Creation Run ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" {
			return fmt.Errorf("creation %s 需要 --id", args[0])
		}
		var run domain.CreationRun
		var err error
		if args[0] == "pause" {
			run, err = api.PauseCreationRun(ctx, *runID, time.Now().UTC())
		} else {
			run, err = api.CancelCreationRun(ctx, *runID, time.Now().UTC())
		}
		return writeResult(stdout, run, err)
	default:
		return fmt.Errorf("未知 creation 子命令 %q", args[0])
	}
}

func runQuick(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "write" {
		return fmt.Errorf("quick 需要 write 子命令")
	}
	flags := newFlags("quick write", stderr)
	projectID := flags.String("project", "", "Project ID")
	userID := flags.String("user", "", "User ID")
	premise := flags.String("premise", "", "一句话创作要求")
	chapters := flags.Int("chapters", 3, "连续创作章节数")
	approval := flags.String("approval", "", "审批预设 auto/milestone/manual；留空沿用进行中的创作")
	workerID := flags.String("worker", "quick-worker", "Worker instance ID")
	lease := flags.Duration("lease", time.Minute, "每个 Operation 的 Worker lease")
	var packIDs, profileRefs stringValues
	flags.Var(&packIDs, "pack", "启用 Pack ID；可重复")
	flags.Var(&profileRefs, "creator-profile", "启用 id@scope Creator Profile；可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *premise == "" {
		return fmt.Errorf("quick write 需要 --project --user --premise")
	}
	packs := make([]service.PackRef, len(packIDs))
	for i, id := range packIDs {
		packs[i] = service.PackRef{ID: id}
	}
	profiles, err := parseCreatorProfileRefs(profileRefs)
	if err != nil {
		return err
	}
	result, err := api.QuickWrite(ctx, service.QuickWriteCommand{
		ProjectID: *projectID, UserID: *userID, Premise: *premise,
		Chapters: *chapters, Approval: domain.ApprovalPolicy(*approval),
		WorkerID: *workerID, LeaseDuration: *lease,
		Packs: packs, CreatorProfiles: profiles,
		CreatedAt: time.Now().UTC(),
	})
	if err := writeResult(stdout, result, err); err != nil {
		return err
	}
	writeQuickGuidance(ctx, api, result, *userID, stdout)
	return nil
}

// writeQuickGuidance 在等待裁决时给出创作语言的下一步指引（D26/C3）：
// 原始候选与诊断仍可通过列出的命令展开查看。
func writeQuickGuidance(
	ctx context.Context,
	api *service.Service,
	result service.QuickWriteResult,
	userID string,
	stdout io.Writer,
) {
	if result.RunState != domain.RunWaitingUser || result.WaitingOperationID == "" {
		return
	}
	proposal, err := api.ProposalForOperation(ctx, result.WaitingOperationID)
	if err != nil {
		return
	}
	fmt.Fprintf(stdout, "\n%s\n", result.RunReason)
	fmt.Fprintf(stdout, "  查看这一稿：ainovel-cli --headless proposal show --id %s\n", proposal.ID)
	fmt.Fprintf(stdout, "  满意就通过：ainovel-cli --headless proposal approve --id %s --user %s，再重新执行本命令继续写\n", proposal.ID, userID)
	fmt.Fprintf(stdout, "  想改就否决：ainovel-cli --headless proposal reject --id %s --user %s --reason \"想调整的方向\"，重写会带上你的反馈\n", proposal.ID, userID)
}

func runPack(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("pack 需要 install、export 或 eval 子命令")
	}
	switch args[0] {
	case "install":
		flags := newFlags("pack install", stderr)
		directory := flags.String("dir", "", "Pack 开发目录")
		archive := flags.String("file", "", ".novelpack 分发文件")
		remoteURL := flags.String("url", "", ".novelpack 下载 URL")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "安装或更新原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		sources := 0
		for _, source := range []string{*directory, *archive, *remoteURL} {
			if source != "" {
				sources++
			}
		}
		if sources != 1 || *changeID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("pack install 需要在 --dir、--file、--url 中选择一个，并提供 --change --user --reason")
		}
		var installed service.InstalledPack
		var err error
		switch {
		case *directory != "":
			installed, err = api.InstallPackDirectory(ctx, *directory, *changeID, *userID, *reason, time.Now().UTC())
		case *archive != "":
			installed, err = api.InstallPackArchive(ctx, *archive, *changeID, *userID, *reason, time.Now().UTC())
		default:
			installed, err = api.InstallPackURL(ctx, *remoteURL, *changeID, *userID, *reason, time.Now().UTC())
		}
		return writeResult(stdout, installed, err)
	case "export":
		flags := newFlags("pack export", stderr)
		id := flags.String("id", "", "Pack ID")
		revisionText := flags.String("revision", "0", "Pack Revision；0 表示最新")
		path := flags.String("file", "", "输出 .novelpack 文件")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *path == "" {
			return fmt.Errorf("pack export 需要 --id --file")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		if err := api.ExportPack(ctx, service.PackRef{ID: *id, Revision: revision}, *path); err != nil {
			return err
		}
		return writeResult(stdout, struct {
			ID       string          `json:"id"`
			Revision domain.Revision `json:"revision"`
			File     string          `json:"file"`
		}{ID: *id, Revision: revision, File: *path}, nil)
	case "eval":
		flags := newFlags("pack eval", stderr)
		id := flags.String("id", "", "Pack ID")
		revisionText := flags.String("revision", "0", "Pack Revision；0 表示最新")
		outputPath := flags.String("output", "", "待评测正文文件")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *outputPath == "" {
			return fmt.Errorf("pack eval 需要 --id --output")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		output, err := os.ReadFile(*outputPath)
		if err != nil {
			return fmt.Errorf("read eval output: %w", err)
		}
		result, err := api.EvaluatePack(ctx, service.PackRef{ID: *id, Revision: revision}, string(output))
		return writeResult(stdout, result, err)
	default:
		return fmt.Errorf("未知 pack 子命令 %q", args[0])
	}
}

func runProfile(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("profile 需要 save、show、learn、candidates 或 confirm 子命令")
	}
	switch args[0] {
	case "save":
		flags := newFlags("profile save", stderr)
		path := flags.String("file", "", "Creator Profile JSON 文件")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "保存原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || *changeID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("profile save 需要 --file --change --user --reason")
		}
		var profile domain.CreatorProfile
		if err := DecodeFile(*path, &profile); err != nil {
			return err
		}
		changeSet, err := api.SaveCreatorProfile(ctx, *changeID, *userID, *reason, profile, time.Now().UTC())
		return writeResult(stdout, changeSet, err)
	case "show":
		flags := newFlags("profile show", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		revisionText := flags.String("revision", "0", "Revision；0 表示最新")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" {
			return fmt.Errorf("profile show 需要 --id --scope")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		profile, storedRevision, err := api.CreatorProfile(ctx, *id, *scope, revision)
		return writeResult(stdout, struct {
			Revision domain.Revision       `json:"revision"`
			Profile  domain.CreatorProfile `json:"profile"`
		}{Revision: storedRevision, Profile: profile}, err)
	case "learn":
		flags := newFlags("profile learn", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		candidate := flags.String("candidate", "", "Preference Candidate ID")
		projectID := flags.String("project", "", "来源 Project ID")
		fromText := flags.String("from", "", "修改前 Revision")
		toText := flags.String("to", "", "修改后 Revision")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" || *candidate == "" || *projectID == "" || *fromText == "" || *toText == "" {
			return fmt.Errorf("profile learn 需要 --id --scope --candidate --project --from --to")
		}
		from, err := parseRevision(*fromText)
		if err != nil {
			return err
		}
		to, err := parseRevision(*toText)
		if err != nil {
			return err
		}
		record, err := api.LearnPreference(ctx, service.LearnPreferenceCommand{
			CandidateID: *candidate, ProfileID: *id, Scope: *scope, ProjectID: *projectID,
			FromRevision: from, ToRevision: to, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, record, err)
	case "candidates":
		flags := newFlags("profile candidates", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" {
			return fmt.Errorf("profile candidates 需要 --id --scope")
		}
		candidates, err := api.PreferenceCandidates(ctx, *id, *scope)
		return writeResult(stdout, candidates, err)
	case "confirm":
		flags := newFlags("profile confirm", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		candidate := flags.String("candidate", "", "Preference Candidate ID")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "确认原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" || *candidate == "" || *changeID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("profile confirm 需要 --id --scope --candidate --change --user --reason")
		}
		changeSet, err := api.ConfirmPreferenceCandidate(
			ctx, *changeID, *userID, *reason, *id, *scope, *candidate, time.Now().UTC(),
		)
		return writeResult(stdout, changeSet, err)
	default:
		return fmt.Errorf("未知 profile 子命令 %q", args[0])
	}
}

func runProject(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("project 需要 create、show、export、import、delete、derived、revert、approval、overlay、assets、directive、lock 或 unlock 子命令")
	}
	switch args[0] {
	case "directive":
		return runProjectDirective(ctx, api, args[1:], stdout, stderr)
	case "delete":
		flags := newFlags("project delete", stderr)
		projectID := flags.String("project", "", "Project ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" {
			return fmt.Errorf("project delete 需要 --project")
		}
		if err := api.DeleteProject(ctx, *projectID); err != nil {
			return err
		}
		return writeResult(stdout, map[string]string{"deleted": *projectID}, nil)
	case "approval":
		flags := newFlags("project approval", stderr)
		projectID := flags.String("project", "", "Project ID")
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		policy := flags.String("policy", "", "审批策略 auto、milestone 或 manual")
		reason := flags.String("reason", "", "调整审批策略的原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *policy == "" || *reason == "" {
			return fmt.Errorf("project approval 需要 --project --user --policy --reason")
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("approval:%s:%d", *policy, time.Now().UnixMilli())
		}
		result, err := api.SetApprovalPolicy(ctx, service.SetApprovalPolicyCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Policy: domain.ApprovalPolicy(*policy), Reason: *reason,
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "overlay":
		flags := newFlags("project overlay", stderr)
		projectID := flags.String("project", "", "Project ID")
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "调整书级规则的原因")
		var rules stringValues
		flags.Var(&rules, "rule", "书级创作规则；可重复，一条不传即移除 Overlay")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("project overlay 需要 --project --user --reason")
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("overlay:%d", time.Now().UnixMilli())
		}
		result, err := api.SetProjectOverlay(ctx, service.SetProjectOverlayCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Rules: rules, Reason: *reason, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "assets":
		flags := newFlags("project assets", stderr)
		projectID := flags.String("project", "", "Project ID")
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "调整固定资产的原因")
		var packRefs, profileRefs stringValues
		flags.Var(&packRefs, "pack", "启用的 Pack：id 或 id@revision；可重复")
		flags.Var(&profileRefs, "profile", "启用的 Creator Profile：id/scope 或 id/scope@revision；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("project assets 需要 --project --user --reason；--pack/--profile 全不传即移除固定引用")
		}
		packs := make([]service.PackRef, 0, len(packRefs))
		for _, ref := range packRefs {
			id, revision, err := splitAssetRef(ref)
			if err != nil {
				return fmt.Errorf("--pack %q: %w", ref, err)
			}
			packs = append(packs, service.PackRef{ID: id, Revision: revision})
		}
		profiles := make([]service.CreatorProfileRef, 0, len(profileRefs))
		for _, ref := range profileRefs {
			key, revision, err := splitAssetRef(ref)
			if err != nil {
				return fmt.Errorf("--profile %q: %w", ref, err)
			}
			id, scope, ok := strings.Cut(key, "/")
			if !ok {
				return fmt.Errorf("--profile %q 需要 id/scope 形式", ref)
			}
			profiles = append(profiles, service.CreatorProfileRef{ID: id, Scope: scope, Revision: revision})
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("assets:%d", time.Now().UnixMilli())
		}
		result, err := api.SetProjectAssets(ctx, service.SetProjectAssetsCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Packs: packs, CreatorProfiles: profiles, Reason: *reason, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "lock", "unlock":
		flags := newFlags("project "+args[0], stderr)
		projectID := flags.String("project", "", "Project ID")
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "调整所有权的原因")
		docRef := flags.String("doc", "", "目标文档 kind/id，如 canon/fact-1、plan/chapter-plan-2、intent/root")
		level := flags.String("level", "locked", "锁定强度 locked 或 guided")
		var guidance stringValues
		flags.Var(&guidance, "guidance", "guided 时的创作指引；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		kind, id, ok := strings.Cut(*docRef, "/")
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || !ok {
			return fmt.Errorf("project %s 需要 --project --user --reason --doc kind/id", args[0])
		}
		control := domain.ControlLevel(*level)
		if args[0] == "unlock" {
			control = domain.ControlOpen
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("ownership:%s/%s:%d", kind, id, time.Now().UnixMilli())
		}
		result, err := api.SetOwnership(ctx, service.SetOwnershipCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Target:  domain.DocumentRef{Kind: domain.DocumentKind(kind), ID: id},
			Control: control, Guidance: guidance, Reason: *reason,
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "create":
		flags := newFlags("project create", stderr)
		projectID := flags.String("project", "", "Project ID")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "创建原因")
		draftPath := flags.String("draft", "", "详细大纲 JSON 文件")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *changeID == "" || *userID == "" || *reason == "" || *draftPath == "" {
			return fmt.Errorf("project create 需要 --project --change --user --reason --draft")
		}
		var draft service.ProjectDraft
		if err := DecodeFile(*draftPath, &draft); err != nil {
			return err
		}
		project, err := api.CreateProject(ctx, service.CreateProjectCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Reason: *reason, Draft: draft, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, project, err)
	case "show":
		flags := newFlags("project show", stderr)
		projectID := flags.String("project", "", "Project ID")
		revisionText := flags.String("revision", "0", "Revision；0 表示最新")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return fmt.Errorf("project show 需要 --project")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		project, err := api.Project(ctx, *projectID, revision)
		return writeResult(stdout, project, err)
	case "export":
		flags := newFlags("project export", stderr)
		projectID := flags.String("project", "", "Project ID")
		revisionText := flags.String("revision", "0", "Revision；0 表示最新")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return fmt.Errorf("project export 需要 --project")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		projection, err := api.ExportProject(ctx, *projectID, revision)
		return writeResult(stdout, projection, err)
	case "derived":
		flags := newFlags("project derived", stderr)
		projectID := flags.String("project", "", "Project ID")
		revisionText := flags.String("revision", "0", "Revision；0 表示最新")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return fmt.Errorf("project derived 需要 --project")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		documents, err := api.DerivedDocuments(ctx, *projectID, revision)
		return writeResult(stdout, documents, err)
	case "import":
		flags := newFlags("project import", stderr)
		path := flags.String("file", "", "Project Projection JSONC 文件")
		proposalID := flags.String("proposal", "", "Proposal ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "修改原因")
		semantic := flags.Bool("semantic", false, "调用模型分析语义影响并生成三种处理选项")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || *proposalID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("project import 需要 --file --proposal --user --reason")
		}
		var projection service.ProjectProjection
		if err := DecodeFile(*path, &projection); err != nil {
			return err
		}
		var proposal domain.Proposal
		var err error
		if *semantic {
			proposal, err = api.ImportProjectWithSemantic(
				ctx, *proposalID, *userID, *reason, projection, time.Now().UTC(),
			)
		} else {
			proposal, err = api.ImportProject(
				ctx, *proposalID, *userID, *reason, projection, time.Now().UTC(),
			)
		}
		return writeResult(stdout, proposal, err)
	case "revert":
		flags := newFlags("project revert", stderr)
		projectID := flags.String("project", "", "Project ID")
		proposalID := flags.String("proposal", "", "Proposal ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "回滚原因")
		revisionText := flags.String("to", "", "目标历史 Revision")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || *proposalID == "" || *userID == "" || *reason == "" || *revisionText == "" {
			return fmt.Errorf("project revert 需要 --project --proposal --user --reason --to")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		proposal, err := api.PrepareRevert(
			ctx, *proposalID, *projectID, *userID, *reason, revision, time.Now().UTC(),
		)
		return writeResult(stdout, proposal, err)
	default:
		return fmt.Errorf("未知 project 子命令 %q", args[0])
	}
}

func runProposal(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("proposal 需要 show、approve、reject 或 resolve 子命令")
	}
	if args[0] == "resolve" {
		flags := newFlags("proposal resolve", stderr)
		id := flags.String("id", "", "Proposal ID")
		userID := flags.String("user", "", "User ID")
		strategy := flags.String("strategy", "", "rewrite_affected、reinterpret_future 或 abandon")
		runID := flags.String("run", "", "rewrite_affected 所属 Creation Run ID")
		reason := flags.String("reason", "", "裁决反馈；放弃候选时会进入后续重写任务")
		modelDigest := flags.String("model-digest", "", "模型配置摘要；已配置 Runtime 时可省略")
		coreVersion := flags.String("core", "core-v1", "Core Protocol 版本")
		var packIDs, profileRefs stringValues
		flags.Var(&packIDs, "pack", "受影响章节重写使用的 Pack ID；可重复")
		flags.Var(&profileRefs, "creator-profile", "受影响章节重写使用的 id@scope Profile；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *userID == "" || *strategy == "" || flags.NArg() != 0 {
			return fmt.Errorf("proposal resolve 需要 --id --user --strategy")
		}
		packs := make([]service.PackRef, len(packIDs))
		for index, packID := range packIDs {
			packs[index] = service.PackRef{ID: packID}
		}
		profiles, err := parseCreatorProfileRefs(profileRefs)
		if err != nil {
			return err
		}
		result, err := api.ResolveProposal(ctx, service.ResolveProposalCommand{
			ProposalID: *id, UserID: *userID, Strategy: *strategy, Reason: *reason,
			RunID: *runID,
			Packs: packs, CreatorProfiles: profiles, CoreProtocolVersion: *coreVersion,
			ModelConfigDigest: *modelDigest, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	}
	flags := newFlags("proposal "+args[0], stderr)
	id := flags.String("id", "", "Proposal ID")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "否决理由；会进入后续重写任务")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("proposal %s 需要 --id", args[0])
	}
	switch args[0] {
	case "show":
		proposal, err := api.Proposal(ctx, *id)
		return writeResult(stdout, proposal, err)
	case "approve":
		if *userID == "" {
			return fmt.Errorf("proposal approve 需要 --user")
		}
		changeSet, err := api.Approve(ctx, *id, *userID, time.Now().UTC())
		return writeResult(stdout, changeSet, err)
	case "reject":
		if *userID == "" {
			return fmt.Errorf("proposal reject 需要 --user")
		}
		proposal, err := api.Reject(ctx, *id, *userID, *reason, time.Now().UTC())
		return writeResult(stdout, proposal, err)
	default:
		return fmt.Errorf("未知 proposal 子命令 %q", args[0])
	}
}

func runOperation(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("operation 需要 start、restart、run、show、events、pause、resume、cancel、priority 或 recover 子命令")
	}
	switch args[0] {
	case "start":
		flags := newFlags("operation start", stderr)
		id := flags.String("id", "", "Operation ID")
		projectID := flags.String("project", "", "Project ID")
		runID := flags.String("run", "", "内容类任务所属 Creation Run ID")
		kind := flags.String("kind", "", "Operation kind")
		worker := flags.String("profile", "", "Worker Profile ID；默认由 Operation kind 决定")
		inputPath := flags.String("input", "", "任务输入 JSON 文件")
		policy := flags.String("approval", "manual", "auto、milestone 或 manual；custom 需未来的显式策略契约")
		modelDigest := flags.String("model-digest", "", "模型配置摘要")
		priority := flags.Int("priority", 0, "优先级")
		var packIDs, profileRefs, dependencyIDs stringValues
		flags.Var(&packIDs, "pack", "启用 Pack ID；可重复，启动时冻结最新 Revision")
		flags.Var(&profileRefs, "creator-profile", "启用 id@scope Creator Profile；可重复")
		flags.Var(&dependencyIDs, "depends-on", "依赖的 Operation ID；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		operationKind := domain.OperationKind(*kind)
		if *id == "" || *projectID == "" || *kind == "" || *inputPath == "" ||
			(operationKind.RequiresCreationRun() && *runID == "") {
			return fmt.Errorf("operation start 需要 --id --project --kind --input；内容类任务还需要 --run")
		}
		input, err := os.ReadFile(*inputPath)
		if err != nil {
			return fmt.Errorf("read operation input: %w", err)
		}
		packs := make([]service.PackRef, len(packIDs))
		for i, id := range packIDs {
			packs[i] = service.PackRef{ID: id}
		}
		profiles, err := parseCreatorProfileRefs(profileRefs)
		if err != nil {
			return err
		}
		operation, err := api.StartOperation(ctx, service.StartOperationCommand{
			OperationID: *id, ProjectID: *projectID, Kind: operationKind, RunID: *runID,
			WorkerProfileID: *worker, Priority: *priority, Input: input,
			Packs: packs, CreatorProfiles: profiles, DependsOn: dependencyIDs,
			CoreProtocolVersion: "core-v1", ModelConfigDigest: *modelDigest,
			ApprovalPolicy: domain.ApprovalPolicy(*policy), CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, operation, err)
	case "restart":
		flags := newFlags("operation restart", stderr)
		fromID := flags.String("from", "", "来源 Operation ID")
		id := flags.String("id", "", "新 Operation ID")
		worker := flags.String("profile", "", "新 Worker Profile ID；默认沿用原职责")
		policy := flags.String("approval", "", "新审批策略；默认沿用")
		modelDigest := flags.String("model-digest", "", "新模型配置摘要；默认使用当前 Runtime")
		coreVersion := flags.String("core", "", "Core Protocol 版本；默认沿用")
		var packIDs, profileRefs stringValues
		flags.Var(&packIDs, "pack", "使用最新 Pack Revision；可重复；不传则沿用旧 Revision")
		flags.Var(&profileRefs, "creator-profile", "使用最新 id@scope Profile；可重复；不传则沿用旧 Revision")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *fromID == "" || *id == "" || flags.NArg() != 0 {
			return fmt.Errorf("operation restart 需要 --from --id")
		}
		var packs []service.PackRef
		if len(packIDs) != 0 {
			packs = make([]service.PackRef, len(packIDs))
			for i, packID := range packIDs {
				packs[i] = service.PackRef{ID: packID}
			}
		}
		var profiles []service.CreatorProfileRef
		var err error
		if len(profileRefs) != 0 {
			profiles, err = parseCreatorProfileRefs(profileRefs)
			if err != nil {
				return err
			}
		}
		operation, err := api.RestartOperation(ctx, service.RestartOperationCommand{
			FromOperationID: *fromID, OperationID: *id, WorkerProfileID: *worker,
			Packs: packs, CreatorProfiles: profiles, CoreProtocolVersion: *coreVersion,
			ModelConfigDigest: *modelDigest, ApprovalPolicy: domain.ApprovalPolicy(*policy),
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, operation, err)
	case "run":
		flags := newFlags("operation run", stderr)
		id := flags.String("id", "", "指定 Operation ID；留空则执行队列中的下一个")
		worker := flags.String("worker", "", "Worker instance ID")
		lease := flags.Duration("lease", time.Minute, "Worker lease")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *worker == "" {
			return fmt.Errorf("operation run 需要 --worker")
		}
		var result any
		var err error
		if *id == "" {
			result, err = api.RunNextOperation(ctx, *worker, *lease, time.Now().UTC())
		} else {
			result, err = api.RunOperation(ctx, *id, *worker, *lease, time.Now().UTC())
		}
		return writeResult(stdout, result, err)
	case "show":
		flags := newFlags("operation show", stderr)
		id := flags.String("id", "", "Operation ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("operation show 需要 --id")
		}
		operation, err := api.Operation(ctx, *id)
		return writeResult(stdout, operation, err)
	case "events":
		flags := newFlags("operation events", stderr)
		id := flags.String("id", "", "Operation ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("operation events 需要 --id")
		}
		events, err := api.OperationEvents(ctx, *id)
		return writeResult(stdout, events, err)
	case "pause", "resume", "cancel":
		flags := newFlags("operation "+args[0], stderr)
		id := flags.String("id", "", "Operation ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("operation %s 需要 --id", args[0])
		}
		var operation domain.Operation
		var err error
		switch args[0] {
		case "pause":
			operation, err = api.PauseOperation(ctx, *id, time.Now().UTC())
		case "resume":
			operation, err = api.ResumeOperation(ctx, *id, time.Now().UTC())
		case "cancel":
			operation, err = api.CancelOperation(ctx, *id, time.Now().UTC())
		}
		return writeResult(stdout, operation, err)
	case "priority":
		flags := newFlags("operation priority", stderr)
		id := flags.String("id", "", "Operation ID")
		priority := flags.Int("value", 0, "新优先级")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("operation priority 需要 --id --value")
		}
		operation, err := api.ReprioritizeOperation(ctx, *id, *priority, time.Now().UTC())
		return writeResult(stdout, operation, err)
	case "recover":
		flags := newFlags("operation recover", stderr)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		ids, err := api.RecoverOperations(ctx, time.Now().UTC())
		return writeResult(stdout, ids, err)
	default:
		return fmt.Errorf("未知 operation 子命令 %q", args[0])
	}
}

func runPrompt(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("prompt 需要 show、sources、diff、lint 或 reload 子命令")
	}
	if args[0] == "reload" {
		flags := newFlags("prompt reload", stderr)
		projectID := flags.String("project", "", "Project ID")
		kind := flags.String("kind", "", "Operation kind，用于构建对应故事上下文")
		worker := flags.String("profile", "", "Worker Profile ID；默认由 Operation kind 决定")
		inputPath := flags.String("input", "", "任务输入 JSON 文件")
		policy := flags.String("approval", "manual", "auto、milestone 或 manual；custom 需未来的显式策略契约")
		modelDigest := flags.String("model-digest", "", "模型配置摘要")
		var packIDs, profileRefs stringValues
		flags.Var(&packIDs, "pack", "启用 Pack ID；可重复，编译时冻结最新 Revision")
		flags.Var(&profileRefs, "creator-profile", "启用 id@scope Creator Profile；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" || *kind == "" || *inputPath == "" {
			return fmt.Errorf("prompt reload 需要 --project --kind --input")
		}
		input, err := os.ReadFile(*inputPath)
		if err != nil {
			return fmt.Errorf("read prompt task input: %w", err)
		}
		profiles, err := parseCreatorProfileRefs(profileRefs)
		if err != nil {
			return err
		}
		packs := make([]service.PackRef, len(packIDs))
		for i, id := range packIDs {
			packs[i] = service.PackRef{ID: id}
		}
		result, err := api.ReloadPrompt(ctx, service.ReloadPromptCommand{
			ProjectID: *projectID, Kind: domain.OperationKind(*kind), WorkerProfileID: *worker, Input: input,
			Packs: packs, CreatorProfiles: profiles, CoreProtocolVersion: "core-v1",
			ModelConfigDigest: *modelDigest, ApprovalPolicy: domain.ApprovalPolicy(*policy),
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	}
	if args[0] == "diff" {
		flags := newFlags("prompt diff", stderr)
		left := flags.String("left", "", "旧 Execution Profile Digest")
		right := flags.String("right", "", "新 Execution Profile Digest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *left == "" || *right == "" {
			return fmt.Errorf("prompt diff 需要 --left --right")
		}
		diff, err := api.PromptDiff(ctx, *left, *right)
		return writeResult(stdout, diff, err)
	}
	if args[0] == "lint" {
		flags := newFlags("prompt lint", stderr)
		digest := flags.String("profile", "", "Execution Profile Digest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *digest == "" {
			return fmt.Errorf("prompt lint 需要 --profile")
		}
		diagnostics, err := api.PromptLint(ctx, *digest)
		return writeResult(stdout, diagnostics, err)
	}
	if args[0] != "show" && args[0] != "sources" {
		return fmt.Errorf("未知 prompt 子命令 %q", args[0])
	}
	flags := newFlags("prompt "+args[0], stderr)
	digest := flags.String("profile", "", "Execution Profile Digest")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *digest == "" {
		return fmt.Errorf("prompt %s 需要 --profile", args[0])
	}
	text, sources, err := api.Prompt(ctx, *digest)
	if err != nil {
		return err
	}
	if args[0] == "show" {
		_, err = fmt.Fprintln(stdout, text)
		return err
	}
	return writeResult(stdout, sources, nil)
}

// DecodeFile 解析 JSONC 文件（供 Headless 与 TUI 共用同一导入语义）。
func DecodeFile(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JSONC file: %w", err)
	}
	standard, err := hujson.Standardize(payload)
	if err != nil {
		return fmt.Errorf("parse JSONC file: %w", err)
	}
	if err := domain.DecodeStrict(standard, target); err != nil {
		return fmt.Errorf("decode JSON file: %w", err)
	}
	return nil
}

func parseRevision(value string) (domain.Revision, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid revision %q: %w", value, domain.ErrInvalid)
	}
	return domain.Revision(parsed), nil
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

type stringValues []string

func (values *stringValues) String() string {
	return strings.Join(*values, ",")
}

func (values *stringValues) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("flag value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func parseCreatorProfileRefs(values []string) ([]service.CreatorProfileRef, error) {
	refs := make([]service.CreatorProfileRef, 0, len(values))
	for _, value := range values {
		id, scope, ok := strings.Cut(value, "@")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(scope) == "" {
			return nil, fmt.Errorf("creator profile %q must use id@scope", value)
		}
		refs = append(refs, service.CreatorProfileRef{ID: id, Scope: scope})
	}
	return refs, nil
}

// splitAssetRef 解析 name 或 name@revision 形式的资产引用；不带版本表示固定为当前版本。
func splitAssetRef(ref string) (string, domain.Revision, error) {
	name, version, ok := strings.Cut(ref, "@")
	if strings.TrimSpace(name) == "" {
		return "", 0, fmt.Errorf("引用不能为空")
	}
	if !ok {
		return name, 0, nil
	}
	value, err := strconv.ParseInt(version, 10, 64)
	if err != nil || value <= 0 {
		return "", 0, fmt.Errorf("版本必须是正整数")
	}
	return name, domain.Revision(value), nil
}

// runProjectDirective 维护用户创作要求（§4.9）：add 入账、retire 退役、list 列出（含已退役）。
func runProjectDirective(ctx context.Context, api *service.Service, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("project directive 需要 add、retire 或 list 子命令")
	}
	flags := newFlags("project directive "+args[0], stderr)
	projectID := flags.String("project", "", "Project ID")
	switch args[0] {
	case "add":
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "提出要求的原因")
		scope := flags.String("scope", domain.DirectiveScopeProject, "作用域：project、plan_node:<id>、chapter_range:<from>-<to> 或 from_chapter:<n>")
		text := flags.String("text", "", "创作要求原话")
		targetWords := flags.Int("target-words", 0, "目标字数（±10%）")
		minWords := flags.Int("min-words", 0, "最少字数")
		maxWords := flags.Int("max-words", 0, "最多字数")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || *text == "" {
			return fmt.Errorf("project directive add 需要 --project --user --reason --text")
		}
		var constraints *domain.DirectiveConstraints
		if *targetWords > 0 || *minWords > 0 || *maxWords > 0 {
			constraints = &domain.DirectiveConstraints{TargetWords: *targetWords, MinWords: *minWords, MaxWords: *maxWords}
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("directive:%d", time.Now().UnixMilli())
		}
		result, err := api.AddDirective(ctx, service.AddDirectiveCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Scope: *scope, Text: *text, Constraints: constraints, Reason: *reason,
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "retire":
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "退役要求的原因")
		directiveID := flags.String("id", "", "Directive ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || *directiveID == "" {
			return fmt.Errorf("project directive retire 需要 --project --user --reason --id")
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("directive-retire:%s:%d", *directiveID, time.Now().UnixMilli())
		}
		result, err := api.RetireDirective(ctx, service.RetireDirectiveCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			DirectiveID: *directiveID, Reason: *reason, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "list":
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" {
			return fmt.Errorf("project directive list 需要 --project")
		}
		project, err := api.Project(ctx, *projectID, 0)
		if err != nil {
			return err
		}
		return writeResult(stdout, append([]domain.Directive{}, project.Directives...), nil)
	}
	return fmt.Errorf("未知的 project directive 子命令 %q", args[0])
}

func writeResult(stdout io.Writer, value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeHelp(output io.Writer) error {
	_, err := fmt.Fprintln(output, strings.TrimSpace(`ainovel-cli v1 commands:
  quick write
  creation start|show|strategy|pause|cancel|events
  project create|show|export|import|delete|derived|revert|lock|unlock|approval|overlay|assets|directive add|retire|list
  proposal show|approve|reject|resolve
  operation start|restart|run|show|events|pause|resume|cancel|priority|recover
  prompt show|sources|diff|lint|reload
  pack install|export|eval
  profile save|show|learn|candidates|confirm`))
	return err
}
