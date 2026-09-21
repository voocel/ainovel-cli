package headless

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func runPack(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
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
		var installed resource.InstalledPack
		var err error
		switch {
		case *directory != "":
			installed, err = api.Resources.InstallPackDirectory(ctx, *directory, *changeID, *userID, *reason, time.Now().UTC())
		case *archive != "":
			installed, err = api.Resources.InstallPackArchive(ctx, *archive, *changeID, *userID, *reason, time.Now().UTC())
		default:
			installed, err = api.Resources.InstallPackURL(ctx, *remoteURL, *changeID, *userID, *reason, time.Now().UTC())
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
		if err := api.Resources.ExportPack(ctx, resource.PackRef{ID: *id, Revision: revision}, *path); err != nil {
			return err
		}
		return writeResult(stdout, struct {
			ID       string         `json:"id"`
			Revision model.Revision `json:"revision"`
			File     string         `json:"file"`
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
		result, err := api.Resources.EvaluatePack(ctx, resource.PackRef{ID: *id, Revision: revision}, string(output))
		return writeResult(stdout, result, err)
	default:
		return fmt.Errorf("未知 pack 子命令 %q", args[0])
	}
}
