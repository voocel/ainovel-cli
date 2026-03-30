package main

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/ui/tui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	configPath, args := parseFlags()

	// 首次引导
	if bootstrap.NeedsSetup(configPath) {
		setupCfg, err := bootstrap.RunSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			os.Exit(1)
		}
		// 引导完成后使用生成的配置继续
		runWithConfig(setupCfg, args)
		return
	}

	// 加载配置
	cfg, err := bootstrap.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	runWithConfig(cfg, args)
}

func runWithConfig(cfg bootstrap.Config, args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "error: 不再支持命令行直接传入小说需求，请启动后在 TUI 输入框中输入")
		os.Exit(1)
	}

	bundle := assets.Load(cfg.Style)
	if err := tui.Run(cfg, bundle); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseFlags 提取 --config 参数，返回配置路径和剩余参数。
func parseFlags() (configPath string, args []string) {
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			i++
			continue
		}
		args = append(args, os.Args[i])
	}
	return
}
