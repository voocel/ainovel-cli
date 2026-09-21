package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/voocel/ainovel-cli/internal/app/binding"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/tui"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

var version = "v1-dev"

const usage = `ainovel-cli — AI 小说创作

用法：
  ainovel-cli                     进入创作界面（TUI，人类主入口）
  ainovel-cli --headless <动作>   无交互执行动作，供脚本与外部集成使用
  ainovel-cli --headless help     查看全部无交互动作
  ainovel-cli --version           显示版本

可选参数：
  --db <路径>                 作品库数据库位置（默认 ~/.ainovel/v1/ainovel.db）
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ainovel-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stdout, usage) }
	showVersion := flags.Bool("version", false, "显示版本")
	headlessMode := flags.Bool("headless", false, "无交互执行动作，供脚本与外部集成使用")
	databasePath := flags.String("db", "", "作品库数据库路径；默认 ~/.ainovel/v1/ainovel.db")
	if err := flags.Parse(args); err != nil {
		// --help 是程序级入口（workbench §3）：输出帮助属正常退出。
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	commands := flags.Args()

	// 入口契约（workbench §3）：外层只区分交互形态。无参数只进 TUI；
	// 非交互动作必须显式进入 Headless，不提供第三条路径。
	if len(commands) != 0 && !*headlessMode {
		return fmt.Errorf("非交互命令需要显式 headless 模式：ainovel-cli --headless %s ...", commands[0])
	}
	if *headlessMode && len(commands) == 0 {
		return headless.Run(context.Background(), nil, nil, stdout, stderr)
	}
	if *headlessMode {
		switch commands[0] {
		case "quick", "creation", "project", "proposal", "operation", "prompt", "pack", "profile", "artifact", "diag", "model", "help":
		default:
			return fmt.Errorf("未知命令 %q", commands[0])
		}
		if commands[0] == "help" {
			return headless.Run(context.Background(), nil, commands, stdout, stderr)
		}
		if commands[0] == "diag" {
			return runDiagnostics(context.Background(), *databasePath, commands, stdout, stderr)
		}
	}

	configDir, err := appconfig.DefaultDir()
	if err != nil {
		return err
	}
	// 配置解析失败不锁死 TUI（Codex 复审 #2）：交互模式降级进向导修复，
	// Headless 面向脚本保持硬失败。
	var configError string
	config, err := appconfig.ResolveConfig(configDir)
	if err != nil {
		if *headlessMode {
			return err
		}
		configError = err.Error()
		config, _ = appconfig.LoadConfig(configDir)
	}
	dataPath := *databasePath
	if dataPath == "" {
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return fmt.Errorf("create app data dir: %w", err)
		}
		dataPath = appconfig.DataPath(configDir)
	}
	authorityStore, err := store.Open(context.Background(), dataPath)
	if err != nil {
		return err
	}
	api := bootstrap.New(authorityStore, bootstrap.Options{Version: version, ConfigDir: configDir, Interactive: !*headlessMode})
	// 模型绑定失败与配置解析失败同一口径：交互模式降级进向导，Headless 硬失败。
	if err := api.Models.Apply(config); err != nil && config.Configured() {
		if *headlessMode {
			authorityStore.Close()
			return err
		}
		configError = err.Error()
	}

	// 终止信号取消 ctx：在途创作收到取消后把任务放回队列再退出，不留悬空租约。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var runErr error
	if *headlessMode {
		runErr = headless.Run(ctx, api, commands, stdout, stderr)
	} else {
		runErr = tui.Run(ctx, tui.Deps{
			API:         api,
			ConfigDir:   configDir,
			UserID:      appconfig.DefaultUserID(),
			ConfigError: configError,
			Verify:      binding.Verify,
			Input:       os.Stdin,
			Output:      stdout,
		})
	}
	closeErr := authorityStore.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

// Diagnostic startup must work even when model configuration is broken. Opening
// the authority read-only also prevents diagnosis from migrating or recovering it.
func runDiagnostics(ctx context.Context, path string, args []string, stdout, stderr io.Writer) error {
	if path == "" {
		dir, err := appconfig.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "诊断数据库路径不可用：%v\n", err)
			return headless.Run(ctx, bootstrap.NewDiagnostics(nil, version, "path_unavailable"), args, stdout, stderr)
		}
		path = appconfig.DataPath(dir)
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		fmt.Fprintf(stderr, "诊断数据库不可用：%v\n", err)
		code := "open_failed"
		var openError *store.DiagnosticOpenError
		if errors.As(err, &openError) {
			code = openError.Code
		}
		return headless.Run(ctx, bootstrap.NewDiagnostics(nil, version, code), args, stdout, stderr)
	}
	err = headless.Run(ctx, bootstrap.NewDiagnostics(s, version), args, stdout, stderr)
	closeErr := s.Close()
	if err != nil {
		return err
	}
	return closeErr
}
