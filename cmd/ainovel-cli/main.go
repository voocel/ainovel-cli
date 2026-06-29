package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/tui"
	"github.com/voocel/ainovel-cli/internal/rules"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// headlessMode 记录本次Có czy không headless 启动，供 die 决定LỗiThoát时Có czy khôngTạm dừng。
var headlessMode bool

func main() {
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
	if bootstrap.NeedsSetup(opts.ConfigPath) {
		if opts.Headless {
			die("error: headless 模式Không hỗ trợ首次引导，Vui lòng先运行一次 TUI Hoàn thànhCấu hình")
		}
		setupCfg, err := bootstrap.RunSetup()
		if err != nil {
			die("setup: %v", err)
		}
		// 引导Hoàn thành后使用生成的Cấu hìnhTiếp tục
		runWithConfig(setupCfg, opts, args)
		return
	}

	// 加载Cấu hình
	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		die("config: %v", err)
	}

	runWithConfig(cfg, opts, args)
}

// die 统一处理致命LỗiThoát：打印到 stderr、落盘到 ~/.ainovel/last-error.log，
// 并在交互式终端（非 headless）下Tạm dừng等待回车——双击启动时控制台会随进程Thoát
// 立即关闭，不Tạm dừng的话Lỗi一闪而过，正是 issue #37 里用户Không có从排查的根因。
func die(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	if path := bootstrap.WriteStartupError(msg); path != "" {
		fmt.Fprintf(os.Stderr, "（详细Lỗi已记录到 %s）\n", path)
	}
	if !headlessMode && stdinIsTerminal() {
		fmt.Fprint(os.Stderr, "\n按回车键Thoát...")
		fmt.Fscanln(os.Stdin)
	}
	os.Exit(1)
}

// stdinIsTerminal 判断标准NhậpCó czy không连接到终端（字符设备）。双击启动 / 交互式终端
// 为 true；管道、重定向、CI 为 false。零依赖近似，足够区分要不要Tạm dừng。
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
		die("error: 不再支持命令行直接传入小说需求，Vui lòng启动后在 TUI Nhập框中Nhập")
	}

	bundle := assets.Load(cfg.Style)
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
	ConfigPath    string
	Headless      bool
	Prompt        string
	PromptFile    string
	Version       bool
	Update        bool
	UpdateVersion string
}

// parseCLIOptions 提取 CLI flag，Quay lại选项和Còn lại参数。
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
					return opts, nil, fmt.Errorf("update 只接受一个可选Phiên bản参数")
				}
				opts.UpdateVersion = argv[i+1]
				i++
			}
			if i+1 < len(argv) {
				return opts, nil, fmt.Errorf("update 只接受一个可选Phiên bản参数")
			}
		case "--config":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--config Thiếu值")
			}
			opts.ConfigPath = argv[i+1]
			i++
		case "--headless":
			opts.Headless = true
		case "--prompt":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt Thiếu值")
			}
			opts.Prompt = argv[i+1]
			i++
		case "--prompt-file":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt-file Thiếu值")
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
	if opts.Version && (opts.Update || opts.ConfigPath != "" || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || len(args) > 0) {
		return opts, nil, fmt.Errorf("version 不能与Khác启动参数混用")
	}
	if opts.Update && (opts.ConfigPath != "" || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || len(args) > 0) {
		return opts, nil, fmt.Errorf("update 不能与Khác启动参数混用")
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
		fmt.Printf("ainovel-cli Đã là bản mới nhất %s\n", result.Version)
		return nil
	}
	fmt.Printf("ainovel-cli 已更Mới到 %s\n", result.Version)
	fmt.Printf("安装位置：%s\n", result.Path)
	return nil
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
		return "", fmt.Errorf("Đọc prompt Thất bại: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
