package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/tui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	opts, args, err := parseCLIOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "flags: %v\n", err)
		os.Exit(1)
	}

	// 首次引导
	if bootstrap.NeedsSetup(opts.ConfigPath) {
		if opts.Headless {
			fmt.Fprintln(os.Stderr, "error: headless 模式不支持首次引导，请先运行一次 TUI 完成配置")
			os.Exit(1)
		}
		setupCfg, err := bootstrap.RunSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			os.Exit(1)
		}
		// 引导完成后使用生成的配置继续
		runWithConfig(setupCfg, opts, args)
		return
	}

	// 加载配置
	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	runWithConfig(cfg, opts, args)
}

func runWithConfig(cfg bootstrap.Config, opts cliOptions, args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "error: 不再支持命令行直接传入小说需求，请启动后在 TUI 输入框中输入")
		os.Exit(1)
	}

	bundle := assets.Load(cfg.Style)
	if opts.Headless {
		prompt, err := loadPrompt(opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err := headless.Run(cfg, bundle, headless.Options{Prompt: prompt}); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if opts.Prompt != "" || opts.PromptFile != "" {
		fmt.Fprintln(os.Stderr, "error: --prompt/--prompt-file 仅能在 --headless 模式下使用")
		os.Exit(1)
	}
	if err := tui.Run(cfg, bundle); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type cliOptions struct {
	ConfigPath string
	Headless   bool
	Prompt     string
	PromptFile string
}

// parseCLIOptions 提取 CLI flag，返回选项和剩余参数。
func parseCLIOptions(argv []string) (cliOptions, []string, error) {
	var opts cliOptions
	var args []string
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--config":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--config 缺少值")
			}
			opts.ConfigPath = argv[i+1]
			i++
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
	return opts, args, nil
}

func loadPrompt(opts cliOptions) (string, error) {
	if opts.PromptFile == "" {
		return strings.TrimSpace(opts.Prompt), nil
	}

	var data []byte
	var err error
	if opts.PromptFile == "-" {
		data, err = os.ReadFile("/dev/stdin")
	} else {
		data, err = os.ReadFile(opts.PromptFile)
	}
	if err != nil {
		return "", fmt.Errorf("读取 prompt 失败: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
