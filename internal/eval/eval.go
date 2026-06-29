package eval

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// Command 是 `ainovel-cli eval` 子命令入口，返回进程退出码：
// 0=PASS/WARN，1=有 case FAIL，2=用法/配置错误。
//
// 清晰流程：加载配置 → 加载 case → 应用 variant 覆盖 → 逐 case 运行 → 采集 → 评分 → 聚合 → 报告。
func Command(argv []string) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	casesPath := fs.String("cases", "", "case 目录或单个 .json 文件（必填）")
	variantDir := fs.String("variant", "", "variant prompt 覆盖目录（含 writer.md 等核心提示词）")
	configPath := fs.String("config", "", "配置文件路径（缺省用默认路径）")
	outDir := fs.String("out", "", "报告输出目录（缺省 workspace/evals/<run_id>）")
	maxChapters := fs.Int("max-chapters", -1, "覆盖所有 case 的章数上限（-1=不覆盖）")
	timeout := fs.Duration("timeout", 30*time.Minute, "单 case 墙钟上限（0=不限）")
	ci := fs.Bool("ci", false, "CI 模式：抑制逐事件进度输出，仅打印最终结论（退出码已反映门禁，无需此 flag 也生效）")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if strings.TrimSpace(*casesPath) == "" {
		fmt.Fprintln(os.Stderr, "eval: 缺少 --cases")
		fs.Usage()
		return 2
	}

	cfg, err := bootstrap.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: 加载配置失败: %v\n", err)
		return 2
	}

	cases, err := LoadCases(*casesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: 加载 case 失败: %v\n", err)
		return 2
	}

	variantPrompts, err := loadVariant(*variantDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: 加载 variant 失败: %v\n", err)
		return 2
	}

	runID := time.Now().Format("20060102-150405")
	if *outDir == "" {
		*outDir = filepath.Join("workspace", "evals", runID)
	}
	variantName := ""
	if *variantDir != "" {
		variantName = filepath.Base(*variantDir)
	}

	fmt.Fprintf(os.Stderr, "eval run %s · %d cases · variant=%s\n", runID, len(cases), orNone(variantName))

	results := make([]Result, 0, len(cases))
	for _, c := range cases {
		if *maxChapters >= 0 {
			c.MaxChapters = *maxChapters
		}
		fmt.Fprintf(os.Stderr, "\n▶ %s (%s)\n", c.ID, c.Category)

		style := c.Style
		if style == "" {
			style = cfg.Style
		}
		bundle := assets.Load(style)
		if err := applyVariant(&bundle, variantPrompts); err != nil {
			fmt.Fprintf(os.Stderr, "eval: variant 覆盖失败: %v\n", err)
			return 2
		}

		var progressW io.Writer
		if !*ci {
			progressW = os.Stderr // CI 模式静默逐事件输出，保持日志干净
		}
		caseDir := filepath.Join(*outDir, "artifacts", c.ID)
		runErr := RunCase(cfg, bundle, c, RunOptions{
			OutputDir: caseDir,
			Timeout:   *timeout,
			Progress:  progressW,
		})

		col := Collect(caseDir, runErr)
		res := Grade(c, col)
		results = append(results, res)
		fmt.Fprintf(os.Stderr, "  → %s\n", res.Outcome)
	}

	suite := Aggregate(runID, "single", variantName, results)
	if err := WriteReport(suite, *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "eval: 写报告失败: %v\n", err)
		return 2
	}

	fmt.Fprintf(os.Stderr, "\n%s\n报告: %s\n", Summary(suite), filepath.Join(*outDir, "report.md"))
	if suite.Gate == Fail {
		return 1
	}
	return 0
}

// loadVariant 读取 variant 目录下所有 *.md（文件名→内容）。空目录返回空 map。
func loadVariant(dir string) (map[string]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(data)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("variant 目录无 *.md 文件: %s", dir)
	}
	return out, nil
}

func applyVariant(b *assets.Bundle, prompts map[string]string) error {
	for file, raw := range prompts {
		if err := b.OverridePrompt(file, raw); err != nil {
			return err
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}
