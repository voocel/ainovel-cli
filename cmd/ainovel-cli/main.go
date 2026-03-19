package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/orchestrator"
	"github.com/voocel/ainovel-cli/internal/ui/tui"
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
	bundle := assets.Load(cfg.Style)
	prompt := strings.Join(args, " ")
	if prompt != "" {
		// CLI 模式：有命令行参数，直接运行
		cfg.Prompt = prompt
		if err := orchestrator.Run(cfg, bundle); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// TUI 模式：无命令行参数，启动交互界面
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
