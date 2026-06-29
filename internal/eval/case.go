// Package eval 是 ainovel-cli 的离线评测 harness。
//
// 设计立足点：评测器（确定性诊断 diag、全书文体 stylestat、七维 rubric）项目里已经
// 存在，eval 只做薄薄一层——批量驱动 case、采集产出、把 diag Finding 与 case 契约映射
// 成门禁、聚合报告。一份事实定义，不在评测层重写一遍判断。详见 docs/evaluation-system.md。
//
// 本期（Phase 1 MVP）只做单次运行的绝对门禁：diag Findings + expect 契约 → PASS/WARN/FAIL。
// A/B delta、stylestat 回归、LLM Judge 是之后的纯加法层。
package eval

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// caseIDPattern 限制 case id 为安全字符：id 会拼进输出目录并被 RunCase 的 RemoveAll 清理，
// 禁止 . / 等路径字符，杜绝 "../" 路径穿越删到工作区外。
var caseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Case 是一个评测样本：一段创作需求 + 一组事实层断言。
type Case struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`       // 评测层：smoke/workflow/quality/longform/recovery/steering
	Role          string   `json:"role,omitempty"` // 被测角色：writer/architect/editor/coordinator（与 Category 正交）
	Description   string   `json:"description,omitempty"`
	Prompt        string   `json:"prompt"`                   // 用户创作需求
	Style         string   `json:"style,omitempty"`          // 覆盖配置风格
	MaxChapters   int      `json:"max_chapters"`             // 章数上限；0 表示只跑到规划完成（进入 writing）
	TargetPrompts []string `json:"target_prompts,omitempty"` // 本 case 主要验证的 prompt 文件（信息性）
	Rubric        string   `json:"rubric,omitempty"`         // LLM Judge 评分表（Phase 3 启用）
	Expect        Expect   `json:"expect"`
	Gate          Gate     `json:"gate"`
}

// Expect 是 case 级契约断言——只声明 diag 通用规则覆盖不到、与本 case 强相关的预期。
type Expect struct {
	Phase                string   `json:"phase,omitempty"`                  // 期望最终 phase
	MinCompletedChapters int      `json:"min_completed_chapters,omitempty"` // 至少完成的章数
	RequiredCheckpoints  []string `json:"required_checkpoints,omitempty"`   // 形如 "chapter:1:commit" / "arc:1:1:arc_summary" / "global:layered_outline"
	NoPending            []string `json:"no_pending,omitempty"`             // 结束时应清空的信号：pending_commit/pending_steer/last_commit/last_review
}

// Gate 是本 case 的门禁阈值。本期只用 MaxSeverity；其余字段为 A/B（regression）阶段预留，
// 解析但不参与门禁——保留是为了 case 文件能按 docs/evaluation-system.md 的完整 schema 书写。
type Gate struct {
	MaxSeverity string `json:"max_severity,omitempty"` // diag Finding 允许的最高严重度（默认 warning）：超过即 hard fail

	MaxCostDeltaRatio     float64 `json:"max_cost_delta_ratio,omitempty"`
	MaxToolCallDeltaRatio float64 `json:"max_tool_call_delta_ratio,omitempty"`
	StylestatRegression   string  `json:"stylestat_regression,omitempty"`
}

// Validate 校验 case 必填字段。
func (c *Case) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("case 缺少 id")
	}
	if !caseIDPattern.MatchString(c.ID) {
		return fmt.Errorf("case id 非法 %q：仅允许小写字母/数字/下划线/连字符，且不含路径字符", c.ID)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return fmt.Errorf("case %q 缺少 prompt", c.ID)
	}
	if c.Gate.MaxSeverity == "" {
		c.Gate.MaxSeverity = "warning"
	}
	if !validSeverity(c.Gate.MaxSeverity) {
		return fmt.Errorf("case %q 的 gate.max_severity 非法: %s", c.ID, c.Gate.MaxSeverity)
	}
	return nil
}

// LoadCases 从单个 .json 文件或目录加载 case。目录下所有 *.json 递归加载，按 id 排序。
func LoadCases(path string) ([]Case, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".json") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		files = []string{path}
	}

	var cases []Case
	seen := map[string]string{}
	for _, f := range files {
		c, err := loadCaseFile(f)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("case id 重复: %q（%s 与 %s）", c.ID, prev, f)
		}
		seen[c.ID] = f
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("未找到任何 case: %s", path)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

func loadCaseFile(path string) (Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // 拼错字段直接报错，避免静默忽略
	if err := dec.Decode(&c); err != nil {
		return Case{}, fmt.Errorf("解析 case %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Case{}, err
	}
	return c, nil
}
