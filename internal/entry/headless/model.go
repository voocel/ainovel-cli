package headless

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/voocel/ainovel-cli/internal/app/binding"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// runModel 是模型绑定命令组：查看、列出、切换连接/模型与思考强度，按角色可覆盖。
// 模型名可能自带斜杠（OpenRouter），所以连接用 --connection 指定而不是斜杠语法。
func runModel(_ context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if api == nil || api.Models == nil {
		return fmt.Errorf("model 命令需要已装配的应用")
	}
	if len(args) == 0 {
		selections := make([]binding.Selection, 0, 4)
		for _, role := range api.Models.Roles() {
			selections = append(selections, api.Models.Current(role))
		}
		return writeResult(stdout, selections, nil)
	}
	switch args[0] {
	case "list":
		return writeResult(stdout, api.Models.Choices(), nil)
	case "use":
		flags := newFlags("model use", stderr)
		role := flags.String("role", "", "角色：architect、writer 或 editor；留空是默认绑定")
		connection := flags.String("connection", "", "连接名；留空沿用当前连接")
		inherit := flags.Bool("inherit", false, "清除该角色的覆盖，跟随默认")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *inherit {
			if *role == "" || flags.NArg() != 0 {
				return fmt.Errorf("model use --inherit 需要 --role")
			}
			if err := api.Models.Use(*role, "", ""); err != nil {
				return err
			}
			return writeResult(stdout, api.Models.Current(*role), nil)
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("model use 需要一个模型名或 model list 里的编号")
		}
		target, connectionName := flags.Arg(0), *connection
		if index, err := strconv.Atoi(target); err == nil {
			choices := api.Models.Choices()
			if index < 1 || index > len(choices) {
				return fmt.Errorf("编号 %d 超出范围（1–%d）", index, len(choices))
			}
			target, connectionName = choices[index-1].Model, choices[index-1].Connection
		}
		if err := api.Models.Use(*role, connectionName, target); err != nil {
			return err
		}
		return writeResult(stdout, api.Models.Current(*role), nil)
	case "effort":
		flags := newFlags("model effort", stderr)
		role := flags.String("role", "", "角色：architect、writer 或 editor；留空是默认绑定")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("model effort 需要一个档位：auto / off / minimal / low / medium / high / xhigh / max，角色可用 inherit")
		}
		if err := api.Models.SetThinking(*role, flags.Arg(0)); err != nil {
			return err
		}
		return writeResult(stdout, api.Models.Current(*role), nil)
	default:
		return fmt.Errorf("model 需要 list、use 或 effort 子命令")
	}
}
