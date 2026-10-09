package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func runCreation(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("creation 需要 start、show、strategy、pause、cancel 或 events 子命令")
	}
	switch args[0] {
	case "start":
		flags := newFlags("creation start", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		projectID := flags.String("project", "", "Project ID")
		premise := flags.String("premise", "", "创作目标")
		chapters := flags.Int("chapters", 0, "全书章数；0 表示交给 AI")
		repairBudget := flags.Int("repair-budget", novel.DefaultRepairBudget, "每章允许的自动修订次数")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" || *projectID == "" || *premise == "" || *chapters < 0 || *repairBudget < 0 {
			return fmt.Errorf("creation start 需要 --id --project --premise、非负 --chapters 和非负 --repair-budget")
		}
		project, err := api.Projects.Project(ctx, *projectID, model.InitialRevision)
		if err != nil {
			return err
		}
		approval := project.Approval
		if approval == "" {
			approval = model.ApprovalAuto
		}
		strategy := model.CreationRunStrategy{ReviewCadence: model.ReviewPerPlanWindow, AutoRepairBudget: *repairBudget}
		preset, err := model.NewCreationRunPreset("custom", approval, strategy)
		if err != nil {
			return err
		}
		run, err := api.Runs.StartCreationRun(ctx, creation.StartCreationRunCommand{
			RunID: *runID, ProjectID: *projectID,
			Goal:     model.NovelGoal{Premise: *premise, TargetChapters: *chapters}.Goal(),
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
		run, err := api.Runs.CreationRun(ctx, *runID)
		return writeResult(stdout, run, err)
	case "strategy":
		flags := newFlags("creation strategy", stderr)
		runID := flags.String("id", "", "Creation Run ID")
		repairBudget := flags.Int("repair-budget", -1, "每章允许的自动修订次数")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *runID == "" || *repairBudget < 0 || flags.NArg() != 0 {
			return fmt.Errorf("creation strategy 需要 --id 与非负 --repair-budget")
		}
		run, err := api.Runs.CreationRun(ctx, *runID)
		if err != nil {
			return err
		}
		strategy := run.Strategy
		strategy.AutoRepairBudget = *repairBudget
		updated, err := api.Runs.UpdateCreationRunStrategy(ctx, *runID, strategy, time.Now().UTC())
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
		events, err := api.Runs.CreationRunEvents(ctx, *runID)
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
		var run model.CreationRun
		var err error
		if args[0] == "pause" {
			run, err = api.Runs.PauseCreationRun(ctx, *runID, time.Now().UTC())
		} else {
			run, err = api.Runs.CancelCreationRun(ctx, *runID, time.Now().UTC())
		}
		return writeResult(stdout, run, err)
	default:
		return fmt.Errorf("未知 creation 子命令 %q", args[0])
	}
}
