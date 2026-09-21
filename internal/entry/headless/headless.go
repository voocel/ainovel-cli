package headless

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Storage 是子命令的存储需求，main 据此决定装配深度。
type Storage int

const (
	StorageNone     Storage = iota // 不开库
	StorageReadOnly                // 只读开库：配置损坏也要能诊断，且不触发迁移
	StorageFull                    // 完整装配
)

type handler func(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error

// Command 是命令表的一项：Run 按它分派，帮助由它渲染，main 按 Storage 装配。
type Command struct {
	Name    string
	Summary string
	Usage   string // 帮助里的用法行；多行用换行分隔
	Storage Storage
	run     handler
}

// commands 是唯一的命令表。用函数而非包级变量：help 项要渲染表本身，变量会形成初始化环。
func commands() []Command {
	return []Command{
		{Name: "quick", Summary: "一句话要求连续写几章；等待裁决时给出下一步", Storage: StorageFull, run: runQuick,
			Usage: "quick write"},
		{Name: "creation", Summary: "创作运行：启动、查看、调策略、暂停、取消、事件", Storage: StorageFull, run: runCreation,
			Usage: "creation start|show|strategy|pause|cancel|events"},
		{Name: "project", Summary: "作品：创建、查看、导出导入、回滚、审批策略、书级规则、资产、要求、裁决、锁定", Storage: StorageFull, run: runProject,
			Usage: "project create|show|export|import|delete|derived|revert|lock|unlock|approval|overlay|assets|directive add|retire|list|adjudication add|withdraw|list"},
		{Name: "proposal", Summary: "提案：查看、通过、否决、裁决语义冲突", Storage: StorageFull, run: runProposal,
			Usage: "proposal show|approve|reject|resolve"},
		{Name: "operation", Summary: "任务：启动、重启、执行、查看、事件、暂停、恢复、取消、优先级、回收过期租约", Storage: StorageFull, run: runOperation,
			Usage: "operation start|restart|run|show|events|pause|resume|cancel|priority|recover"},
		{Name: "prompt", Summary: "提示词：查看、来源、对比、检查、重新编译", Storage: StorageFull, run: runPrompt,
			Usage: "prompt show|sources|diff|lint|reload"},
		{Name: "pack", Summary: "Pack：安装、导出、评测", Storage: StorageFull, run: runPack,
			Usage: "pack install|export|eval"},
		{Name: "profile", Summary: "Creator Profile：保存、查看、学习偏好、候选、确认", Storage: StorageFull, run: runProfile,
			Usage: "profile save|show|learn|candidates|confirm"},
		{Name: "artifact", Summary: "工件：列出、回收无引用对象", Storage: StorageFull, run: runArtifact,
			Usage: "artifact list|gc"},
		{Name: "model", Summary: "模型绑定：查看、列出、切换连接与模型、思考强度；按角色可覆盖", Storage: StorageFull, run: runModel,
			Usage: "model [list|use [--role R] [--connection C] <模型|编号>|use --role R --inherit|effort [--role R] <档位|inherit>]"},
		{Name: "diag", Summary: "诊断：查看环境或作品、运行、任务状态；导出可分享报告", Storage: StorageReadOnly, run: runDiag,
			Usage: "diag [--project ID] [--run ID] [--operation ID] [--after ID] [--event-after N]\n" +
				"diag export [--project ID] [--run ID] [--operation ID] --file diagnostics.json"},
		{Name: "help", Summary: "查看全部无交互动作", Storage: StorageNone, run: runHelp},
	}
}

// Lookup 按名字取命令表项；main 用它决定装配深度，不再自己维护命令名。
func Lookup(name string) (Command, bool) {
	for _, command := range commands() {
		if command.Name == name {
			return command, true
		}
	}
	return Command{}, false
}

func Run(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return writeHelp(stdout)
	}
	command, ok := Lookup(args[0])
	if !ok {
		return fmt.Errorf("未知命令 %q", args[0])
	}
	return command.run(ctx, api, args[1:], stdout, stderr)
}

func runHelp(_ context.Context, _ *bootstrap.App, _ []string, stdout, _ io.Writer) error {
	return writeHelp(stdout)
}

func writeHelp(output io.Writer) error {
	var help strings.Builder
	help.WriteString("ainovel-cli v1 commands:\n")
	for _, command := range commands() {
		fmt.Fprintf(&help, "  %-10s %s\n", command.Name, command.Summary)
		if command.Usage == "" {
			continue
		}
		for _, line := range strings.Split(command.Usage, "\n") {
			fmt.Fprintf(&help, "    %s\n", line)
		}
	}
	_, err := io.WriteString(output, help.String())
	return err
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

func parseRevision(value string) (model.Revision, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid revision %q: %w", value, model.ErrInvalid)
	}
	return model.Revision(parsed), nil
}

// packRefs 把 --pack 列表转成不带版本的引用；调用方按需冻结最新 Revision。
func packRefs(ids []string) []resource.PackRef {
	packs := make([]resource.PackRef, len(ids))
	for i, id := range ids {
		packs[i] = resource.PackRef{ID: id}
	}
	return packs
}

func parseCreatorProfileRefs(values []string) ([]resource.CreatorProfileRef, error) {
	refs := make([]resource.CreatorProfileRef, 0, len(values))
	for _, value := range values {
		id, scope, ok := strings.Cut(value, "@")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(scope) == "" {
			return nil, fmt.Errorf("creator profile %q must use id@scope", value)
		}
		refs = append(refs, resource.CreatorProfileRef{ID: id, Scope: scope})
	}
	return refs, nil
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
