package headless

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/jsonc"
)

func runProject(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("project 需要 create、show、export、import、delete、derived、revert、approval、overlay、assets、directive、adjudication、lock 或 unlock 子命令")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "create":
		return runProjectCreate(ctx, api, rest, stdout, stderr)
	case "show":
		return runProjectShow(ctx, api, rest, stdout, stderr)
	case "export":
		return runProjectExport(ctx, api, rest, stdout, stderr)
	case "import":
		return runProjectImport(ctx, api, rest, stdout, stderr)
	case "delete":
		return runProjectDelete(ctx, api, rest, stdout, stderr)
	case "derived":
		return runProjectDerived(ctx, api, rest, stdout, stderr)
	case "revert":
		return runProjectRevert(ctx, api, rest, stdout, stderr)
	case "approval":
		return runProjectApproval(ctx, api, rest, stdout, stderr)
	case "overlay":
		return runProjectOverlay(ctx, api, rest, stdout, stderr)
	case "assets":
		return runProjectAssets(ctx, api, rest, stdout, stderr)
	case "directive":
		return runProjectDirective(ctx, api, rest, stdout, stderr)
	case "adjudication":
		return runProjectAdjudication(ctx, api, rest, stdout, stderr)
	case "lock", "unlock":
		return runProjectOwnership(ctx, api, verb, rest, stdout, stderr)
	default:
		return fmt.Errorf("未知 project 子命令 %q", verb)
	}
}

func runProjectCreate(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project create", stderr)
	projectID := flags.String("project", "", "Project ID")
	changeID := flags.String("change", "", "Change ID")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "创建原因")
	draftPath := flags.String("draft", "", "详细大纲 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *changeID == "" || *userID == "" || *reason == "" || *draftPath == "" {
		return fmt.Errorf("project create 需要 --project --change --user --reason --draft")
	}
	var draft projectdoc.ProjectDraft
	if err := jsonc.DecodeFile(*draftPath, &draft); err != nil {
		return err
	}
	project, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
		Reason: *reason, Draft: draft, CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, project, err)
}

func runProjectShow(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project show", stderr)
	projectID := flags.String("project", "", "Project ID")
	revisionText := flags.String("revision", "0", "Revision；0 表示最新")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("project show 需要 --project")
	}
	revision, err := parseRevision(*revisionText)
	if err != nil {
		return err
	}
	project, err := api.Projects.Project(ctx, *projectID, revision)
	return writeResult(stdout, project, err)
}

func runProjectExport(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project export", stderr)
	projectID := flags.String("project", "", "Project ID")
	revisionText := flags.String("revision", "0", "Revision；0 表示最新")
	format := flags.String("format", "json", "json 工程投影，或 txt/epub 成品")
	path := flags.String("file", "", "成品输出文件路径")
	title := flags.String("title", "", "导出书名，默认创作意图")
	author := flags.String("author", "", "作者署名")
	from := flags.Int("from", 0, "起始章节号，0 为不限")
	to := flags.Int("to", 0, "结束章节号，0 为不限")
	overwrite := flags.Bool("overwrite", false, "替换已有导出文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("project export 需要 --project")
	}
	revision, err := parseRevision(*revisionText)
	if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("project export 不接受位置参数")
	}
	if *format != "json" {
		result, err := api.Novels.Export(ctx, novel.ExportCommand{ProjectID: *projectID, Revision: revision, Path: *path, Format: *format, Title: *title, Author: *author, From: *from, To: *to, Overwrite: *overwrite})
		return writeResult(stdout, result, err)
	}
	if *path != "" || *title != "" || *author != "" || *from != 0 || *to != 0 || *overwrite {
		return fmt.Errorf("成品参数需要 --format txt 或 --format epub；JSON 投影通过标准输出导出")
	}
	projection, err := api.Projects.ExportProject(ctx, *projectID, revision)
	return writeResult(stdout, projection, err)
}

func runProjectImport(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project import", stderr)
	path := flags.String("file", "", "Project Projection JSONC 文件")
	proposalID := flags.String("proposal", "", "Proposal ID")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "修改原因")
	semantic := flags.Bool("semantic", false, "调用模型分析语义影响并生成三种处理选项")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || *proposalID == "" || *userID == "" || *reason == "" {
		return fmt.Errorf("project import 需要 --file --proposal --user --reason")
	}
	var projection projectdoc.ProjectProjection
	if err := jsonc.DecodeFile(*path, &projection); err != nil {
		return err
	}
	var proposal model.Proposal
	var err error
	if *semantic {
		proposal, err = api.Projects.ImportProjectWithSemantic(
			ctx, *proposalID, *userID, *reason, projection, time.Now().UTC(),
		)
	} else {
		proposal, err = api.Projects.ImportProject(
			ctx, *proposalID, *userID, *reason, projection, time.Now().UTC(),
		)
	}
	return writeResult(stdout, proposal, err)
}

func runProjectDelete(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project delete", stderr)
	projectID := flags.String("project", "", "Project ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" {
		return fmt.Errorf("project delete 需要 --project")
	}
	if err := api.Projects.DeleteProject(ctx, *projectID); err != nil {
		return err
	}
	return writeResult(stdout, map[string]string{"deleted": *projectID}, nil)
}

func runProjectDerived(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project derived", stderr)
	projectID := flags.String("project", "", "Project ID")
	revisionText := flags.String("revision", "0", "Revision；0 表示最新")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("project derived 需要 --project")
	}
	revision, err := parseRevision(*revisionText)
	if err != nil {
		return err
	}
	documents, err := api.Projects.DerivedDocuments(ctx, *projectID, revision)
	return writeResult(stdout, documents, err)
}

func runProjectRevert(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project revert", stderr)
	projectID := flags.String("project", "", "Project ID")
	proposalID := flags.String("proposal", "", "Proposal ID")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "回滚原因")
	revisionText := flags.String("to", "", "目标历史 Revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" || *proposalID == "" || *userID == "" || *reason == "" || *revisionText == "" {
		return fmt.Errorf("project revert 需要 --project --proposal --user --reason --to")
	}
	revision, err := parseRevision(*revisionText)
	if err != nil {
		return err
	}
	proposal, err := api.Decisions.PrepareRevert(
		ctx, *proposalID, *projectID, *userID, *reason, revision, time.Now().UTC(),
	)
	return writeResult(stdout, proposal, err)
}

func runProjectApproval(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project approval", stderr)
	projectID := flags.String("project", "", "Project ID")
	changeID := flags.String("change", "", "Change ID；留空自动生成")
	userID := flags.String("user", "", "User ID")
	policy := flags.String("policy", "", "审批策略 auto、milestone 或 manual")
	reason := flags.String("reason", "", "调整审批策略的原因")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *policy == "" || *reason == "" {
		return fmt.Errorf("project approval 需要 --project --user --policy --reason")
	}
	if *changeID == "" {
		*changeID = fmt.Sprintf("approval:%s:%d", *policy, time.Now().UnixMilli())
	}
	result, err := api.Projects.SetApprovalPolicy(ctx, projectdoc.SetApprovalPolicyCommand{
		ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
		Policy: model.ApprovalPolicy(*policy), Reason: *reason,
		CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, result, err)
}

func runProjectOverlay(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project overlay", stderr)
	projectID := flags.String("project", "", "Project ID")
	changeID := flags.String("change", "", "Change ID；留空自动生成")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "调整书级规则的原因")
	var rules stringValues
	flags.Var(&rules, "rule", "书级创作规则；可重复，一条不传即移除 Overlay")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" {
		return fmt.Errorf("project overlay 需要 --project --user --reason")
	}
	if *changeID == "" {
		*changeID = fmt.Sprintf("overlay:%d", time.Now().UnixMilli())
	}
	result, err := api.Resources.SetProjectOverlay(ctx, resource.SetProjectOverlayCommand{
		ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
		Rules: rules, Reason: *reason, CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, result, err)
}

func runProjectAssets(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project assets", stderr)
	projectID := flags.String("project", "", "Project ID")
	changeID := flags.String("change", "", "Change ID；留空自动生成")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "调整固定资产的原因")
	var packRefs, profileRefs stringValues
	flags.Var(&packRefs, "pack", "启用的 Pack：id 或 id@revision；可重复")
	flags.Var(&profileRefs, "profile", "启用的 Creator Profile：id/scope 或 id/scope@revision；可重复")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" {
		return fmt.Errorf("project assets 需要 --project --user --reason；--pack/--profile 全不传即移除固定引用")
	}
	packs := make([]resource.PackRef, 0, len(packRefs))
	for _, ref := range packRefs {
		id, revision, err := splitAssetRef(ref)
		if err != nil {
			return fmt.Errorf("--pack %q: %w", ref, err)
		}
		packs = append(packs, resource.PackRef{ID: id, Revision: revision})
	}
	profiles := make([]resource.CreatorProfileRef, 0, len(profileRefs))
	for _, ref := range profileRefs {
		key, revision, err := splitAssetRef(ref)
		if err != nil {
			return fmt.Errorf("--profile %q: %w", ref, err)
		}
		id, scope, ok := strings.Cut(key, "/")
		if !ok {
			return fmt.Errorf("--profile %q 需要 id/scope 形式", ref)
		}
		profiles = append(profiles, resource.CreatorProfileRef{ID: id, Scope: scope, Revision: revision})
	}
	if *changeID == "" {
		*changeID = fmt.Sprintf("assets:%d", time.Now().UnixMilli())
	}
	result, err := api.Resources.SetProjectAssets(ctx, resource.SetProjectAssetsCommand{
		ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
		Packs: packs, CreatorProfiles: profiles, Reason: *reason, CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, result, err)
}

// runProjectOwnership 处理 lock 与 unlock：unlock 就是把控制级别置回 open。
func runProjectOwnership(ctx context.Context, api *bootstrap.App, verb string, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("project "+verb, stderr)
	projectID := flags.String("project", "", "Project ID")
	changeID := flags.String("change", "", "Change ID；留空自动生成")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "调整所有权的原因")
	docRef := flags.String("doc", "", "目标文档 kind/id，如 canon/fact-1、plan/chapter-plan-2、intent/root")
	level := flags.String("level", "locked", "锁定强度 locked 或 guided")
	var guidance stringValues
	flags.Var(&guidance, "guidance", "guided 时的创作指引；可重复")
	if err := flags.Parse(args); err != nil {
		return err
	}
	kind, id, ok := strings.Cut(*docRef, "/")
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || !ok {
		return fmt.Errorf("project %s 需要 --project --user --reason --doc kind/id", verb)
	}
	control := model.ControlLevel(*level)
	if verb == "unlock" {
		control = model.ControlOpen
	}
	if *changeID == "" {
		*changeID = fmt.Sprintf("ownership:%s/%s:%d", kind, id, time.Now().UnixMilli())
	}
	result, err := api.Projects.SetOwnership(ctx, projectdoc.SetOwnershipCommand{
		ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
		Target:  model.DocumentRef{Kind: model.DocumentKind(kind), ID: id},
		Control: control, Guidance: guidance, Reason: *reason,
		CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, result, err)
}

// runProjectDirective 维护用户创作要求（§4.9）：add 入账、retire 退役、list 列出（含已退役）。
func runProjectDirective(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
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
		scope := flags.String("scope", model.DirectiveScopeProject, "作用域：project、plan_node:<id>、chapter_range:<from>-<to> 或 from_chapter:<n>")
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
		var constraints *model.DirectiveConstraints
		if *targetWords > 0 || *minWords > 0 || *maxWords > 0 {
			constraints = &model.DirectiveConstraints{TargetWords: *targetWords, MinWords: *minWords, MaxWords: *maxWords}
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("directive:%d", time.Now().UnixMilli())
		}
		result, err := api.Projects.AddDirective(ctx, projectdoc.AddDirectiveCommand{
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
		result, err := api.Projects.RetireDirective(ctx, projectdoc.RetireDirectiveCommand{
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
		project, err := api.Projects.Project(ctx, *projectID, 0)
		if err != nil {
			return err
		}
		return writeResult(stdout, append([]model.Directive{}, project.Directives...), nil)
	}
	return fmt.Errorf("未知的 project directive 子命令 %q", args[0])
}

// runProjectAdjudication 维护用户裁决（D43）：add 接受一条阻塞发现、withdraw 撤回、list 列出全部记录。
func runProjectAdjudication(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("project adjudication 需要 add、withdraw 或 list 子命令")
	}
	flags := newFlags("project adjudication "+args[0], stderr)
	projectID := flags.String("project", "", "Project ID")
	switch args[0] {
	case "add":
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "接受该发现的理由")
		finding := flags.String("finding", "", "发现标识：<审阅 Operation ID>/<发现序号>（见 workbench 或 creation show）")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || *finding == "" {
			return fmt.Errorf("project adjudication add 需要 --project --user --reason --finding")
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("adjudication:%d", time.Now().UnixMilli())
		}
		result, err := api.Reviews.AddAdjudication(ctx, novel.AddAdjudicationCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			Finding: *finding, Reason: *reason, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "withdraw":
		changeID := flags.String("change", "", "Change ID；留空自动生成")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "撤回的理由")
		adjudicationID := flags.String("id", "", "要撤回的裁决 ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" || *userID == "" || *reason == "" || *adjudicationID == "" {
			return fmt.Errorf("project adjudication withdraw 需要 --project --user --reason --id")
		}
		if *changeID == "" {
			*changeID = fmt.Sprintf("adjudication-withdraw:%s:%d", *adjudicationID, time.Now().UnixMilli())
		}
		result, err := api.Reviews.WithdrawAdjudication(ctx, novel.WithdrawAdjudicationCommand{
			ProjectID: *projectID, ChangeID: *changeID, UserID: *userID,
			AdjudicationID: *adjudicationID, Reason: *reason, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	case "list":
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *projectID == "" {
			return fmt.Errorf("project adjudication list 需要 --project")
		}
		project, err := api.Projects.Project(ctx, *projectID, 0)
		if err != nil {
			return err
		}
		return writeResult(stdout, append([]model.Adjudication{}, project.Adjudications...), nil)
	}
	return fmt.Errorf("未知的 project adjudication 子命令 %q", args[0])
}

// splitAssetRef 解析 name 或 name@revision 形式的资产引用；不带版本表示固定为当前版本。
func splitAssetRef(ref string) (string, model.Revision, error) {
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
	return name, model.Revision(value), nil
}
