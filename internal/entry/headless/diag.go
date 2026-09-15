package headless

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/voocel/ainovel-cli/internal/app/diag"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func runDiag(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	export := len(args) != 0 && args[0] == "export"
	if export {
		args = args[1:]
	}
	flags := newFlags("diag", stderr)
	var request diag.Request
	flags.StringVar(&request.ProjectID, "project", "", "作品 ID；留空查看环境")
	flags.StringVar(&request.RunID, "run", "", "创作运行 ID")
	flags.StringVar(&request.OperationID, "operation", "", "任务 ID")
	flags.StringVar(&request.After, "after", "", "浏览任务游标 next_operation_id；分享忽略页码")
	flags.Int64Var(&request.EventAfter, "event-after", 0, "浏览事件游标 next_event_sequence；需要 --operation，分享忽略页码")
	var path string
	if export {
		flags.StringVar(&path, "file", "", "分享报告输出路径；不覆盖现有文件")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("diag 只接受可选的 export 子命令与命名参数")
	}
	if export {
		if path == "" {
			return fmt.Errorf("diag export 需要 --file")
		}
		fmt.Fprintln(stderr, "分享报告包含状态、统计与别名化证据；不包含正文、思考、密钥、原始错误或事件载荷。文件仅保存在本地。")
		if err := api.Diag.ExportShare(ctx, request, path); err != nil {
			return err
		}
		return writeResult(stdout, struct {
			File string `json:"file"`
		}{File: path}, nil)
	}
	report, err := api.Diag.Inspect(ctx, request)
	return writeResult(stdout, report, err)
}
