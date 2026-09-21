package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func runArtifact(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("artifact 需要 list 或 gc 子命令")
	}
	flags := newFlags("artifact "+args[0], stderr)
	switch args[0] {
	case "list":
		projectID := flags.String("project", "", "Project ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *projectID == "" {
			return fmt.Errorf("artifact list 需要 --project")
		}
		artifacts, err := api.Resources.Artifacts(ctx, *projectID)
		return writeResult(stdout, artifacts, err)
	case "gc":
		grace := flags.Duration("grace", 24*time.Hour, "宽限期：比它更新的对象与暂存文件不回收")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		removed, err := api.Resources.CollectArtifactGarbage(ctx, *grace)
		return writeResult(stdout, map[string]int{"removed": removed}, err)
	default:
		return fmt.Errorf("未知 artifact 子命令 %q", args[0])
	}
}
