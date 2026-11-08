package diag

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// UnverifiedReviewRevisions 展示尚未经过原问题复核的返工版本，不自动派单。
func UnverifiedReviewRevisions(snap *Snapshot) []Finding {
	var findings []Finding
	for _, review := range snap.Reviews {
		chapters := review.UnverifiedChapters()
		if len(chapters) == 0 {
			continue
		}
		findings = append(findings, Finding{
			Rule: "UnverifiedReviewRevisions", Category: CatQuality, Severity: SevWarning,
			Confidence: ConfHigh, AutoLevel: AutoNone, Target: "quality.review",
			Title:      fmt.Sprintf("第 %d 章评审之后有 %d 章返工尚未复核", review.Chapter, len(chapters)),
			Evidence:   fmt.Sprintf("修改章节：%v；原评审结论仍属于修改前正文", chapters),
			Suggestion: "查看原评审的 revised_chapters 与当前正文；完本表示写作及摘要收尾完成，不表示这些修改已重新验收。按需人工检查，不自动循环评审。",
		})
	}
	return findings
}

// UnresolvedReviewRecheck 展示一次复核后仍存在的问题，不自动派发第二轮返工。
func UnresolvedReviewRecheck(snap *Snapshot) []Finding {
	var findings []Finding
	for _, r := range snap.Reviews {
		if r.Recheck == nil || r.Recheck.Result == nil || len(r.Recheck.Result.UnresolvedIssues) == 0 {
			continue
		}
		var evidence []string
		for _, issue := range r.Recheck.Result.UnresolvedIssues {
			evidence = append(evidence, fmt.Sprintf("原问题%d: %s", issue.Issue, issue.Evidence))
		}
		findings = append(findings, Finding{
			Rule: "UnresolvedReviewRecheck", Category: CatQuality, Severity: SevWarning,
			Confidence: ConfHigh, AutoLevel: AutoNone, Target: "quality.review",
			Title:      fmt.Sprintf("第 %d 章评审返工复核仍有 %d 项未解决", r.Chapter, len(evidence)),
			Evidence:   strings.Join(evidence, "；"),
			Suggestion: "查看原评审的 recheck.result 与对应正文，按需决定后续修改；单次复核不会自动循环返工。",
		})
	}
	return findings
}

// ChronicLowDimension 检测某评审维度跨多章持续低分。
func ChronicLowDimension(snap *Snapshot) []Finding {
	if len(snap.Reviews) < 2 {
		return nil
	}

	dimSums := make(map[string]float64)
	dimCounts := make(map[string]int)
	for _, r := range snap.Reviews {
		for _, d := range r.Dimensions {
			dimSums[d.Dimension] += float64(d.Score)
			dimCounts[d.Dimension]++
		}
	}

	var findings []Finding
	for name, sum := range dimSums {
		count := dimCounts[name]
		if count < 2 {
			continue
		}
		avg := sum / float64(count)
		if avg >= ThresholdDimScoreLow {
			continue
		}
		findings = append(findings, Finding{
			Rule:       "ChronicLowDimension",
			Category:   CatQuality,
			Severity:   SevWarning,
			Confidence: ConfMedium,
			AutoLevel:  AutoNone,
			Target:     "prompt.writer",
			Title:      fmt.Sprintf("维度 [%s] 持续低分 (均值 %.0f)", name, avg),
			Evidence:   fmt.Sprintf("共 %d 次评审，均分 %.1f", count, avg),
			Suggestion: fmt.Sprintf("检查 Writer prompt 中关于 %s 的指引是否清晰，或 Editor prompt 的 %s 评分标准是否合理。", name, name),
		})
	}
	return findings
}

// ContractMissPattern 检测合同履约率过低。
func ContractMissPattern(snap *Snapshot) []Finding {
	if len(snap.Reviews) == 0 {
		return nil
	}

	var total, missed int
	var missedChapters []string
	for ch, r := range snap.Reviews {
		total++
		if r.ContractStatus == "partial" || r.ContractStatus == "missed" {
			missed++
			missedChapters = append(missedChapters, fmt.Sprintf("ch%d", ch))
		}
	}
	if total == 0 {
		return nil
	}
	rate := float64(missed) / float64(total)
	if rate <= ThresholdContractMissRate {
		return nil
	}
	return []Finding{{
		Rule:       "ContractMissPattern",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("合同履约率低 (%.0f%% 未达成)", rate*100),
		Evidence:   fmt.Sprintf("未达成: [%s]，共 %d/%d", strings.Join(missedChapters, ", "), missed, total),
		Suggestion: "Writer 可能未读 contract，或 contract required_beats 过于激进。检查 plan_chapter 和 writer.md 的配合。",
	}}
}

// HookWeakChain 检测章节 hook 评分连续偏弱。
func HookWeakChain(snap *Snapshot) []Finding {
	if len(snap.Reviews) < ThresholdHookWeakChain {
		return nil
	}

	chapters := sortedChapterReviews(snap)
	var weakChain []int
	for _, ch := range chapters {
		review := snap.Reviews[ch]
		if review == nil || review.Scope != "chapter" {
			continue
		}
		hook := review.Dimension("hook")
		if hook == nil || hook.Score >= ThresholdHookWeakScore {
			if len(weakChain) >= ThresholdHookWeakChain {
				break
			}
			weakChain = weakChain[:0]
			continue
		}
		weakChain = append(weakChain, ch)
	}
	if len(weakChain) < ThresholdHookWeakChain {
		return nil
	}

	var parts []string
	for _, ch := range weakChain {
		if hook := snap.Reviews[ch].Dimension("hook"); hook != nil {
			parts = append(parts, fmt.Sprintf("ch%d(%d)", ch, hook.Score))
		}
	}
	return []Finding{{
		Rule:       "HookWeakChain",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("章末钩子连续偏弱（连续 %d 章）", len(weakChain)),
		Evidence:   strings.Join(parts, ", "),
		Suggestion: "检查 writer.md 中 hook_goal 的执行是否清晰，必要时在 plan_chapter 中明确本章追读欲望，并校准 Editor 对 hook 的举证标准。",
	}}
}

// PayoffMissPattern 检测带 payoff_points 的章节长期未兑现。
func PayoffMissPattern(snap *Snapshot) []Finding {
	var total, missed int
	var details []string
	for ch, plan := range snap.Plans {
		if plan == nil || len(plan.Contract.PayoffPoints) == 0 {
			continue
		}
		review := snap.Reviews[ch]
		if review == nil {
			continue
		}
		total++
		if review.ContractStatus == "partial" || review.ContractStatus == "missed" {
			missed++
			details = append(details, fmt.Sprintf("ch%d(%d项 payoff)", ch, len(plan.Contract.PayoffPoints)))
		}
	}
	if total < 2 {
		return nil
	}
	rate := float64(missed) / float64(total)
	if rate <= ThresholdPayoffMissRate {
		return nil
	}
	sort.Strings(details)
	return []Finding{{
		Rule:       "PayoffMissPattern",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("爽点/情节点兑现率偏低 (%.0f%% 未达成)", rate*100),
		Evidence:   fmt.Sprintf("未兑现章节: [%s]，共 %d/%d", strings.Join(details, ", "), missed, total),
		Suggestion: "检查 plan_chapter 的 payoff_points 是否过多或过空，确保 Writer 在正文里明确兑现，而不是只做铺垫。",
	}}
}

// ExcessiveRewrites 检测改写率过高。
func ExcessiveRewrites(snap *Snapshot) []Finding {
	if len(snap.Reviews) < 2 {
		return nil
	}

	var total, rewrites int
	for _, r := range snap.Reviews {
		total++
		if r.Verdict == "rewrite" {
			rewrites++
		}
	}
	if total == 0 {
		return nil
	}
	rate := float64(rewrites) / float64(total)
	if rate <= ThresholdRewriteRate {
		return nil
	}
	return []Finding{{
		Rule:       "ExcessiveRewrites",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.editor",
		Title:      fmt.Sprintf("改写率过高 (%d/%d = %.0f%%)", rewrites, total, rate*100),
		Evidence:   fmt.Sprintf("共 %d 次评审，%d 次 rewrite", total, rewrites),
		Suggestion: "Writer 持续产出低于 Editor 阈值的内容。检查 Writer prompt 的质量标准是否与 Editor 的评审标准对齐。",
	}}
}

// WordCountAnomaly 检测章节字数异常。
func WordCountAnomaly(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.ChapterWordCounts) < 3 {
		return nil
	}
	wc := snap.Progress.ChapterWordCounts

	var sum float64
	for _, w := range wc {
		sum += float64(w)
	}
	avg := sum / float64(len(wc))
	if avg == 0 {
		return nil
	}

	var anomalies []string
	for ch, w := range wc {
		ratio := float64(w) / avg
		if ratio < ThresholdWordShortRatio {
			anomalies = append(anomalies, fmt.Sprintf("ch%d(%d字,%.0f%%)", ch, w, ratio*100))
		} else if ratio > ThresholdWordLongRatio {
			anomalies = append(anomalies, fmt.Sprintf("ch%d(%d字,%.0f%%)", ch, w, ratio*100))
		}
	}
	if len(anomalies) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "WordCountAnomaly",
		Category:   CatQuality,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "context.window",
		Title:      fmt.Sprintf("章节字数异常 (均值 %d 字)", int(math.Round(avg))),
		Evidence:   strings.Join(anomalies, "; "),
		Suggestion: "极短章节可能是输出截断（token 限制），极长章节可能消耗过多上下文窗口。检查模型 max_tokens 配置。",
	}}
}

func sortedChapterReviews(snap *Snapshot) []int {
	chapters := make([]int, 0, len(snap.Reviews))
	for ch := range snap.Reviews {
		chapters = append(chapters, ch)
	}
	sort.Ints(chapters)
	return chapters
}
