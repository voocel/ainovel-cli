package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/tui"
	"github.com/voocel/ainovel-cli/internal/eval"
	"github.com/voocel/ainovel-cli/internal/rules"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// headlessMode 记录本次是否 headless 启动，供 die 决定错误退出时是否暂停。
var headlessMode bool

func main() {
	// 子命令在常规 flag 解析之前拦截：eval 是离线评测 harness，参数体系独立。
	if len(os.Args) > 1 && os.Args[1] == "eval" {
		os.Exit(eval.Command(os.Args[2:]))
	}

	opts, args, err := parseCLIOptions(os.Args[1:])
	if err != nil {
		die("flags: %v", err)
	}
	if opts.Version {
		buildversion.Print(os.Stdout, versionInfo())
		return
	}
	if opts.Update {
		if err := runSelfUpdate(opts.UpdateVersion); err != nil {
			fmt.Fprintf(os.Stderr, "update: %v\n", err)
			os.Exit(1)
		}
		return
	}
	headlessMode = opts.Headless

	// 首次引导
	if bootstrap.NeedsSetup() {
		if opts.Headless {
			die("error: headless 模式不支持首次引导，请先运行一次 TUI 完成配置")
		}
		setupCfg, err := bootstrap.RunSetup()
		if err != nil {
			die("setup: %v", err)
		}
		// 引导完成后使用生成的配置继续
		runWithConfig(setupCfg, opts, args)
		return
	}

	// 加载配置
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		die("config: %v", err)
	}

	runWithConfig(cfg, opts, args)
}

// die 统一处理致命错误退出：打印到 stderr、落盘到 ~/.ainovel/last-error.log，
// 并在交互式终端（非 headless）下暂停等待回车——双击启动时控制台会随进程退出
// 立即关闭，不暂停的话错误一闪而过，正是 issue #37 里用户无从排查的根因。
func die(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	if path := bootstrap.WriteStartupError(msg); path != "" {
		fmt.Fprintf(os.Stderr, "（详细错误已记录到 %s）\n", path)
	}
	if !headlessMode && stdinIsTerminal() {
		fmt.Fprint(os.Stderr, "\n按回车键退出...")
		fmt.Fscanln(os.Stdin)
	}
	os.Exit(1)
}

// stdinIsTerminal 判断标准输入是否连接到终端（字符设备）。双击启动 / 交互式终端
// 为 true；管道、重定向、CI 为 false。零依赖近似，足够区分要不要暂停。
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runWithConfig(cfg bootstrap.Config, opts cliOptions, args []string) {
	rules.EnsureHomeRulesDir()

	if len(args) > 0 {
		die("error: 不再支持命令行直接传入小说需求，请启动后在 TUI 输入框中输入")
	}

	// FillDefaults 必须先于资产加载:OutputDir 是运行时字段,默认值在此归一——
	// 否则默认配置下 <书目录>/style/ 的本书级文风覆盖永远不会被加载。
	cfg.FillDefaults()
	bundle := assets.Load(cfg.Style, assets.DefaultLoadOptions(cfg.OutputDir))
	if opts.Headless {
		prompt, err := loadPrompt(opts)
		if err != nil {
			die("error: %v", err)
		}
		if err := headless.Run(cfg, bundle, headless.Options{Prompt: prompt}); err != nil {
			die("error: %v", err)
		}
		return
	}
	if opts.Prompt != "" || opts.PromptFile != "" {
		die("error: --prompt/--prompt-file 仅能在 --headless 模式下使用")
	}
	if err := tui.Run(cfg, bundle, versionInfo().Version); err != nil {
		die("error: %v", err)
	}
}

type cliOptions struct {
	Headless      bool
	Prompt        string
	PromptFile    string
	Version       bool
	Update        bool
	UpdateVersion string
}

// parseCLIOptions 提取 CLI flag，返回选项和剩余参数。
func parseCLIOptions(argv []string) (cliOptions, []string, error) {
	var opts cliOptions
	var args []string
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--version", "-v":
			opts.Version = true
		case "version":
			if i+1 < len(argv) {
				return opts, nil, fmt.Errorf("version 不接受参数")
			}
			opts.Version = true
		case "update":
			if opts.Update {
				return opts, nil, fmt.Errorf("update 只能指定一次")
			}
			opts.Update = true
			if i+1 < len(argv) {
				if strings.HasPrefix(argv[i+1], "-") {
					return opts, nil, fmt.Errorf("update 只接受一个可选版本参数")
				}
				opts.UpdateVersion = argv[i+1]
				i++
			}
			if i+1 < len(argv) {
				return opts, nil, fmt.Errorf("update 只接受一个可选版本参数")
			}
		case "--headless":
			opts.Headless = true
		case "--prompt":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt 缺少值")
			}
			opts.Prompt = argv[i+1]
			i++
		case "--prompt-file":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt-file 缺少值")
			}
			opts.PromptFile = argv[i+1]
			i++
		default:
			args = append(args, argv[i])
		}
	}
	if opts.Prompt != "" && opts.PromptFile != "" {
		return opts, nil, fmt.Errorf("--prompt 和 --prompt-file 不能同时使用")
	}
	if opts.Version && (opts.Update || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || len(args) > 0) {
		return opts, nil, fmt.Errorf("version 不能与其他启动参数混用")
	}
	if opts.Update && (opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || len(args) > 0) {
		return opts, nil, fmt.Errorf("update 不能与其他启动参数混用")
	}
	return opts, args, nil
}

func versionInfo() buildversion.Info {
	return buildversion.Resolve(buildversion.Info{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
}

func runSelfUpdate(target string) error {
	info := versionInfo()
	result, err := buildversion.Update(context.Background(), buildversion.UpdateOptions{
		Repo:           "voocel/ainovel-cli",
		BinaryName:     "ainovel-cli",
		TargetVersion:  target,
		CurrentVersion: info.Version,
	})
	if err != nil {
		return err
	}
	if !result.Updated {
		fmt.Printf("ainovel-cli 已是最新版本 %s\n", result.Version)
		return nil
	}
	fmt.Printf("ainovel-cli 已更新到 %s\n", result.Version)
	fmt.Printf("安装位置：%s\n", result.Path)
	return nil
}

func loadPrompt(opts cliOptions) (string, error) {
	return loadPromptFrom(opts, os.Stdin)
}

func loadPromptFrom(opts cliOptions, stdin io.Reader) (string, error) {
	if opts.PromptFile == "" {
		return strings.TrimSpace(opts.Prompt), nil
	}

	var data []byte
	var err error
	if opts.PromptFile == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(opts.PromptFile)
	}
	if err != nil {
		return "", fmt.Errorf("读取 prompt 失败: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
