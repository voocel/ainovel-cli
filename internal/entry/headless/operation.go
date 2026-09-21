package headless

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func runOperation(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("operation 需要 start、restart、run、show、events、pause、resume、cancel、priority 或 recover 子命令")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "start":
		return runOperationStart(ctx, api, rest, stdout, stderr)
	case "restart":
		return runOperationRestart(ctx, api, rest, stdout, stderr)
	case "run":
		return runOperationRun(ctx, api, rest, stdout, stderr)
	case "show":
		return runOperationShow(ctx, api, rest, stdout, stderr)
	case "events":
		return runOperationEvents(ctx, api, rest, stdout, stderr)
	case "pause", "resume", "cancel":
		return runOperationTransition(ctx, api, verb, rest, stdout, stderr)
	case "priority":
		return runOperationPriority(ctx, api, rest, stdout, stderr)
	case "recover":
		return runOperationRecover(ctx, api, rest, stdout, stderr)
	default:
		return fmt.Errorf("未知 operation 子命令 %q", verb)
	}
}

func runOperationStart(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation start", stderr)
	id := flags.String("id", "", "Operation ID")
	projectID := flags.String("project", "", "Project ID")
	runID := flags.String("run", "", "内容类任务所属 Creation Run ID")
	kind := flags.String("kind", "", "Operation kind")
	worker := flags.String("profile", "", "Worker Profile ID；默认由 Operation kind 决定")
	inputPath := flags.String("input", "", "任务输入 JSON 文件")
	policy := flags.String("approval", "manual", "auto、milestone 或 manual；custom 需未来的显式策略契约")
	priority := flags.Int("priority", 0, "优先级")
	var packIDs, profileRefs, dependencyIDs stringValues
	flags.Var(&packIDs, "pack", "启用 Pack ID；可重复，启动时冻结最新 Revision")
	flags.Var(&profileRefs, "creator-profile", "启用 id@scope Creator Profile；可重复")
	flags.Var(&dependencyIDs, "depends-on", "依赖的 Operation ID；可重复")
	if err := flags.Parse(args); err != nil {
		return err
	}
	spec, err := model.KindSpec(model.OperationKind(*kind))
	if err != nil {
		return err
	}
	if *id == "" || *projectID == "" || *inputPath == "" || (spec.RequiresRun && *runID == "") {
		return fmt.Errorf("operation start 需要 --id --project --kind --input；内容类任务还需要 --run")
	}
	input, err := os.ReadFile(*inputPath)
	if err != nil {
		return fmt.Errorf("read operation input: %w", err)
	}
	profiles, err := parseCreatorProfileRefs(profileRefs)
	if err != nil {
		return err
	}
	operation, err := api.Tasks.StartOperation(ctx, task.StartOperationCommand{
		OperationID: *id, ProjectID: *projectID, Kind: spec.Kind, RunID: *runID,
		WorkerProfileID: *worker, Priority: *priority, Input: input,
		Packs: packRefs(packIDs), CreatorProfiles: profiles, DependsOn: dependencyIDs,
		CoreProtocolVersion: "core-v1",
		ApprovalPolicy:      model.ApprovalPolicy(*policy), CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, operation, err)
}

func runOperationRestart(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation restart", stderr)
	fromID := flags.String("from", "", "来源 Operation ID")
	id := flags.String("id", "", "新 Operation ID")
	worker := flags.String("profile", "", "新 Worker Profile ID；默认沿用原职责")
	policy := flags.String("approval", "", "新审批策略；默认沿用")
	coreVersion := flags.String("core", "", "Core Protocol 版本；默认沿用")
	var packIDs, profileRefs stringValues
	flags.Var(&packIDs, "pack", "使用最新 Pack Revision；可重复；不传则沿用旧 Revision")
	flags.Var(&profileRefs, "creator-profile", "使用最新 id@scope Profile；可重复；不传则沿用旧 Revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *fromID == "" || *id == "" || flags.NArg() != 0 {
		return fmt.Errorf("operation restart 需要 --from --id")
	}
	// nil 表示沿用旧 Revision，所以只在显式传入时构造引用。
	var packs []resource.PackRef
	if len(packIDs) != 0 {
		packs = packRefs(packIDs)
	}
	var profiles []resource.CreatorProfileRef
	var err error
	if len(profileRefs) != 0 {
		profiles, err = parseCreatorProfileRefs(profileRefs)
		if err != nil {
			return err
		}
	}
	operation, err := api.Tasks.RestartOperation(ctx, task.RestartOperationCommand{
		FromOperationID: *fromID, OperationID: *id, WorkerProfileID: *worker,
		Packs: packs, CreatorProfiles: profiles, CoreProtocolVersion: *coreVersion,
		ApprovalPolicy: model.ApprovalPolicy(*policy), CreatedAt: time.Now().UTC(),
	})
	return writeResult(stdout, operation, err)
}

func runOperationRun(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation run", stderr)
	id := flags.String("id", "", "指定 Operation ID；留空则执行队列中的下一个")
	worker := flags.String("worker", "", "Worker instance ID")
	lease := flags.Duration("lease", task.DefaultLease, "Worker lease")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *worker == "" {
		return fmt.Errorf("operation run 需要 --worker")
	}
	var result any
	var err error
	if *id == "" {
		result, err = api.Tasks.RunNextOperation(ctx, *worker, *lease, time.Now().UTC())
	} else {
		result, err = api.Tasks.RunOperation(ctx, *id, *worker, *lease, time.Now().UTC())
	}
	return writeResult(stdout, result, err)
}

func runOperationShow(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation show", stderr)
	id := flags.String("id", "", "Operation ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("operation show 需要 --id")
	}
	operation, err := api.Tasks.Operation(ctx, *id)
	return writeResult(stdout, operation, err)
}

func runOperationEvents(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation events", stderr)
	id := flags.String("id", "", "Operation ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("operation events 需要 --id")
	}
	events, err := api.Tasks.OperationEvents(ctx, *id)
	return writeResult(stdout, events, err)
}

// runOperationTransition 处理 pause / resume / cancel 三个只带 --id 的状态迁移。
func runOperationTransition(ctx context.Context, api *bootstrap.App, verb string, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation "+verb, stderr)
	id := flags.String("id", "", "Operation ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("operation %s 需要 --id", verb)
	}
	var operation model.Operation
	var err error
	switch verb {
	case "pause":
		operation, err = api.Tasks.PauseOperation(ctx, *id, time.Now().UTC())
	case "resume":
		operation, err = api.Tasks.ResumeOperation(ctx, *id, time.Now().UTC())
	case "cancel":
		operation, err = api.Tasks.CancelOperation(ctx, *id, time.Now().UTC())
	}
	return writeResult(stdout, operation, err)
}

func runOperationPriority(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation priority", stderr)
	id := flags.String("id", "", "Operation ID")
	priority := flags.Int("value", 0, "新优先级")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("operation priority 需要 --id --value")
	}
	operation, err := api.Tasks.ReprioritizeOperation(ctx, *id, *priority, time.Now().UTC())
	return writeResult(stdout, operation, err)
}

func runOperationRecover(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("operation recover", stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	ids, err := api.Tasks.RecoverOperations(ctx, time.Now().UTC())
	return writeResult(stdout, ids, err)
}
