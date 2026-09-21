package headless

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/profile"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func runPrompt(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("prompt 需要 show、sources、diff、lint 或 reload 子命令")
	}
	if args[0] == "reload" {
		flags := newFlags("prompt reload", stderr)
		projectID := flags.String("project", "", "Project ID")
		kind := flags.String("kind", "", "Operation kind，用于构建对应故事上下文")
		worker := flags.String("profile", "", "Worker Profile ID；默认由 Operation kind 决定")
		inputPath := flags.String("input", "", "任务输入 JSON 文件")
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
		result, err := api.Prompts.ReloadPrompt(ctx, profile.ReloadPromptCommand{
			ProjectID: *projectID, Kind: model.OperationKind(*kind), WorkerProfileID: *worker, Input: input,
			Packs: packRefs(packIDs), CreatorProfiles: profiles, CoreProtocolVersion: "core-v1",
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
		diff, err := api.Prompts.PromptDiff(ctx, *left, *right)
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
		diagnostics, err := api.Prompts.PromptLint(ctx, *digest)
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
	text, sources, err := api.Prompts.Prompt(ctx, *digest)
	if err != nil {
		return err
	}
	if args[0] == "show" {
		_, err = fmt.Fprintln(stdout, text)
		return err
	}
	return writeResult(stdout, sources, nil)
}
