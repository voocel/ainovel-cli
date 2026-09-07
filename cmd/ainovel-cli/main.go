package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/capability"
	"github.com/voocel/ainovel-cli/internal/entry/app"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/tui"
	"github.com/voocel/ainovel-cli/internal/llm/models"
	"github.com/voocel/ainovel-cli/internal/service"
	"github.com/voocel/ainovel-cli/internal/store"
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
		case "quick", "creation", "project", "proposal", "operation", "prompt", "pack", "profile", "help":
		default:
			return fmt.Errorf("未知命令 %q", commands[0])
		}
		if commands[0] == "help" {
			return headless.Run(context.Background(), nil, commands, stdout, stderr)
		}
	}

	configDir, err := app.DefaultDir()
	if err != nil {
		return err
	}
	// 配置解析失败不锁死 TUI（Codex 复审 #2）：交互模式降级进向导修复，
	// Headless 面向脚本保持硬失败。
	var configError string
	config, err := app.ResolveConfig(configDir)
	if err != nil {
		if *headlessMode {
			return err
		}
		configError = err.Error()
		config, _ = app.LoadConfig(configDir)
	}
	dataPath := *databasePath
	if dataPath == "" {
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return fmt.Errorf("create app data dir: %w", err)
		}
		dataPath = app.DataPath(configDir)
	}
	authorityStore, err := store.Open(context.Background(), dataPath)
	if err != nil {
		return err
	}
	api, err := buildService(authorityStore, config, !*headlessMode)
	if err != nil {
		if *headlessMode {
			authorityStore.Close()
			return err
		}
		configError = err.Error()
		api = service.New(authorityStore)
	}
	configured := config.Configured() && configError == ""

	var runErr error
	if *headlessMode {
		runErr = headless.Run(context.Background(), api, commands, stdout, stderr)
	} else {
		runErr = tui.Run(context.Background(), tui.Deps{
			API:           api,
			Configured:    configured,
			ConfigDir:     configDir,
			UserID:        app.DefaultUserID(),
			InitialConfig: config,
			ConfigError:   configError,
			Rebuild: func(next app.Config) (*service.Service, error) {
				return buildService(authorityStore, next, true)
			},
			Verify: func(ctx context.Context, next app.Config) error {
				return models.Verify(ctx, models.Config{
					Provider: next.Provider, Model: next.Model,
					APIKey: next.APIKey, BaseURL: next.BaseURL,
					Timeout: 30 * time.Second,
				})
			},
			Input:  os.Stdin,
			Output: stdout,
		})
	}
	closeErr := authorityStore.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

// buildService 是唯一的服务装配点：无模型配置时返回只读服务（无执行器）；
// 有配置时装配真实 capability Runtime。实时活动通道只在交互入口装配
// （发布与订阅共用同一 Hub），Headless 保持零活动开销。
func buildService(authorityStore *store.Store, config app.Config, withActivity bool) (*service.Service, error) {
	if !config.Configured() {
		return service.New(authorityStore), nil
	}
	modelConfig := models.Config{
		Provider: config.Provider, Model: config.Model,
		APIKey: config.APIKey, BaseURL: config.BaseURL,
	}
	modelDigest, err := modelConfig.Digest()
	if err != nil {
		return nil, err
	}
	model, err := models.New(modelConfig)
	if err != nil {
		return nil, err
	}
	runtime := capability.NewRuntime(model, modelDigest, authorityStore)
	api := service.NewWithExecutor(authorityStore, runtime)
	if withActivity {
		hub := activity.NewHub()
		runtime.SetActivitySink(hub)
		api.AttachActivityFeed(hub)
	}
	return api, nil
}
