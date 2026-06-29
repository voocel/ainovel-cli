package eval

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/diag"
)

// Outcome 是单个 case 的门禁结论。
type Outcome string

const (
	Pass Outcome = "PASS"
	Warn Outcome = "WARN"
	Fail Outcome = "FAIL"
)

// Issue 是门禁判定中的一条记录。
type Issue struct {
	Kind     string `json:"kind"`               // hard_fail / warning / passed
	Source   string `json:"source"`             // runtime / finding:<rule> / contract:<name>
	Severity string `json:"severity,omitempty"` // critical / warning / info
	Detail   string `json:"detail"`
}

// Metrics 是从 diag.Stats 直接借来的概览指标——eval 不重算。
type Metrics struct {
	CompletedChapters int     `json:"completed_chapters"`
	TotalChapters     int     `json:"total_chapters"`
	TotalWords        int     `json:"total_words"`
	AvgWordsPerChap   int     `json:"avg_words_per_chapter"`
	Phase             string  `json:"phase"`
	Flow              string  `json:"flow"`
	ReviewCount       int     `json:"review_count"`
	RewriteCount      int     `json:"rewrite_count"`
	AvgReviewScore    float64 `json:"avg_review_score"`
	CriticalFindings  int     `json:"critical_findings"`
	WarningFindings   int     `json:"warning_findings"`
}

// Result 是单个 case 的完整评测结果。对齐设计稿三层模型：
// HardFails（阻塞）/ Warnings（回归，WARN）/ Notes（信息性，不影响门禁）。
type Result struct {
	CaseID    string  `json:"case_id"`
	Category  string  `json:"category"`
	Role      string  `json:"role,omitempty"`
	Outcome   Outcome `json:"outcome"`
	HardFails []Issue `json:"hard_fails"`
	Warnings  []Issue `json:"warnings"`
	Notes     []Issue `json:"notes,omitempty"`
	Passed    []Issue `json:"passed"`
	Metrics   Metrics `json:"metrics"`
	Dir       string  `json:"dir"`
}

// Grade 把采集结果按 case 契约与 diag Finding 严重度映射成门禁结论。这是 MVP 的核心：
// 确定性证据决定 PASS/WARN/FAIL，不掺主观判断。
func Grade(c Case, col Collected) Result {
	r := Result{
		CaseID:   c.ID,
		Category: c.Category,
		Role:     c.Role,
		Dir:      col.Dir,
		Metrics:  metricsFrom(col.Report),
	}

	// 1. 运行时错误：headless 返回 error 直接 hard fail（失败显式暴露）。
	if col.RuntimeErr != "" {
		r.HardFails = append(r.HardFails, Issue{
			Kind: "hard_fail", Source: "runtime", Detail: "运行时错误: " + col.RuntimeErr,
		})
	}

	// 1b. 工件读取失败：契约依赖的事实读不到，宁可 hard fail 也不 false pass（fail-loud）。
	for _, le := range col.LoadErrors {
		r.HardFails = append(r.HardFails, Issue{
			Kind: "hard_fail", Source: "load", Detail: "工件读取失败: " + le,
		})
	}

	// 2. diag Findings 三层映射（rank 越小越严重）：
	//    超过 max_severity → hard fail；等于 → warning（回归）；低于 → note（信息性，不影响门禁）。
	maxRank := severityRank(c.Gate.MaxSeverity)
	for _, f := range col.Report.Findings {
		sev := string(f.Severity)
		issue := Issue{Source: "finding:" + f.Rule, Severity: sev, Detail: findingDetail(f)}
		switch rank := severityRank(sev); {
		case rank < maxRank:
			issue.Kind = "hard_fail"
			r.HardFails = append(r.HardFails, issue)
		case rank == maxRank:
			issue.Kind = "warning"
			r.Warnings = append(r.Warnings, issue)
		default:
			issue.Kind = "note"
			r.Notes = append(r.Notes, issue)
		}
	}

	// 3. case 契约断言：薄断言，只验本 case 强相关的预期。
	gradeContracts(c, col, &r)

	// 4. 汇总结论。
	switch {
	case len(r.HardFails) > 0:
		r.Outcome = Fail
	case len(r.Warnings) > 0:
		r.Outcome = Warn
	default:
		r.Outcome = Pass
	}
	return r
}

func gradeContracts(c Case, col Collected, r *Result) {
	hardFail := func(source, detail string) {
		r.HardFails = append(r.HardFails, Issue{Kind: "hard_fail", Source: "contract:" + source, Detail: detail})
	}
	pass := func(source, detail string) {
		r.Passed = append(r.Passed, Issue{Kind: "passed", Source: "contract:" + source, Detail: detail})
	}

	e := c.Expect

	if e.Phase != "" {
		got := phaseOf(col)
		if got != e.Phase {
			hardFail("phase", fmt.Sprintf("期望 phase=%s，实际 %s", e.Phase, got))
		} else {
			pass("phase", "phase="+got)
		}
	}

	if e.MinCompletedChapters > 0 {
		got := r.Metrics.CompletedChapters
		if got < e.MinCompletedChapters {
			hardFail("min_completed_chapters", fmt.Sprintf("期望 ≥%d 章，实际 %d 章", e.MinCompletedChapters, got))
		} else {
			pass("min_completed_chapters", fmt.Sprintf("完成 %d 章", got))
		}
	}

	for _, spec := range e.RequiredCheckpoints {
		ok, err := col.HasCheckpoint(spec)
		switch {
		case err != nil:
			hardFail("checkpoint", err.Error())
		case !ok:
			hardFail("checkpoint", "缺少 checkpoint: "+spec)
		default:
			pass("checkpoint", spec)
		}
	}

	for _, sig := range e.NoPending {
		if col.Pending[sig] {
			hardFail("no_pending", "残留信号: "+sig)
		} else {
			pass("no_pending", sig+" 已清空")
		}
	}
}

func metricsFrom(rep diag.Report) Metrics {
	m := Metrics{
		CompletedChapters: rep.Stats.CompletedChapters,
		TotalChapters:     rep.Stats.TotalChapters,
		TotalWords:        rep.Stats.TotalWords,
		AvgWordsPerChap:   rep.Stats.AvgWordsPerCh,
		Phase:             rep.Stats.Phase,
		Flow:              rep.Stats.Flow,
		ReviewCount:       rep.Stats.ReviewCount,
		RewriteCount:      rep.Stats.RewriteCount,
		AvgReviewScore:    rep.Stats.AvgReviewScore,
	}
	for _, f := range rep.Findings {
		switch f.Severity {
		case diag.SevCritical:
			m.CriticalFindings++
		case diag.SevWarning:
			m.WarningFindings++
		}
	}
	return m
}

// phaseOf 优先取 progress 的 phase，回落到 diag.Stats（两者同源）。
func phaseOf(col Collected) string {
	if col.Progress != nil {
		return string(col.Progress.Phase)
	}
	return col.Report.Stats.Phase
}

func findingDetail(f diag.Finding) string {
	if f.Evidence != "" {
		return f.Title + "（" + f.Evidence + "）"
	}
	return f.Title
}

// ── 严重度 ─────────────────────────────────────────────

var severityRanks = map[string]int{"critical": 0, "warning": 1, "info": 2}

func validSeverity(s string) bool {
	_, ok := severityRanks[s]
	return ok
}

// severityRank 越小越严重；未知严重度按最不严重处理，避免误判 hard fail。
func severityRank(s string) int {
	if r, ok := severityRanks[s]; ok {
		return r
	}
	return 99
}
