package diag

import (
	"fmt"
	"sort"

	"github.com/voocel/ainovel-cli/internal/store"
)

// ── 诊断阈值 ─────────────────────────────────────────────

const (
	ThresholdDimScoreLow      = 70   // ChronicLowDimension: 维度均分低于此值告警
	ThresholdContractMissRate = 0.3  // ContractMissPattern: 合同未达成率上限
	ThresholdRewriteRate      = 0.5  // ExcessiveRewrites: 改写率上限
	ThresholdWordShortRatio   = 0.4  // WordCountAnomaly: 字数低于均值此比例视为异常
	ThresholdWordLongRatio    = 2.5  // WordCountAnomaly: 字数高于均值此比例视为异常
	ThresholdCompassDrift     = 15   // CompassDrift: 指南针未更新章数上限
	ThresholdTimelineGapRate  = 0.3  // TimelineGaps: 缺失率容忍上限
	ThresholdForeshadowMin    = 8    // StaleForeshadow: 伏笔停滞最小章数
)

// allRules 按 flow → quality → planning → context 排列。
var allRules = []RuleFunc{
	// Flow
	StuckRewriteLoop,
	OrphanedSteer,
	PhaseFlowMismatch,
	ChapterGaps,
	// Quality
	ChronicLowDimension,
	ContractMissPattern,
	ExcessiveRewrites,
	WordCountAnomaly,
	// Planning
	StaleForeshadow,
	CompassDrift,
	OutlineExhausted,
	MissingSummaries,
	// Context
	GhostCharacter,
	TimelineGaps,
	RelationshipStagnation,
}

// Analyze 是诊断系统的唯一入口。
func Analyze(s *store.Store) Report {
	snap := Load(s)

	var findings []Finding
	for _, e := range snap.LoadErrors {
		findings = append(findings, Finding{
			Rule:       "LoadError",
			Category:   CatFlow,
			Severity:   SevWarning,
			Title:      fmt.Sprintf("工件加载失败: %s", e),
			Suggestion: "文件可能损坏或权限不足，相关诊断规则的结果可能不完整。",
		})
	}
	for _, rule := range allRules {
		findings = append(findings, rule(&snap)...)
	}
	sortFindings(findings)

	return Report{
		Stats:    buildStats(&snap),
		Findings: findings,
	}
}

func buildStats(snap *Snapshot) Stats {
	st := Stats{}
	if snap.Progress == nil {
		return st
	}
	p := snap.Progress
	st.CompletedChapters = len(p.CompletedChapters)
	st.TotalChapters = p.TotalChapters
	st.TotalWords = p.TotalWordCount
	st.Phase = string(p.Phase)
	st.Flow = string(p.Flow)

	if st.CompletedChapters > 0 {
		st.AvgWordsPerCh = st.TotalWords / st.CompletedChapters
	}

	if snap.RunMeta != nil {
		st.PlanningTier = string(snap.RunMeta.PlanningTier)
	}

	// 评审统计
	st.ReviewCount = len(snap.Reviews)
	var totalScore float64
	var dimCount int
	for _, r := range snap.Reviews {
		if r.Verdict == "rewrite" {
			st.RewriteCount++
		}
		for _, d := range r.Dimensions {
			totalScore += float64(d.Score)
			dimCount++
		}
	}
	if dimCount > 0 {
		st.AvgReviewScore = totalScore / float64(dimCount)
	}

	// 伏笔统计
	latest := snap.LatestCompleted()
	for _, f := range snap.Foreshadow {
		if f.Status == "planted" || f.Status == "advanced" {
			st.ForeshadowOpen++
			if f.Status == "planted" && latest-f.PlantedAt > staleForeshadowThreshold(st.CompletedChapters) {
				st.ForeshadowStale++
			}
		}
	}
	return st
}

// sortFindings 按严重程度排序：critical > warning > info。
func sortFindings(findings []Finding) {
	order := map[Severity]int{SevCritical: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(findings, func(i, j int) bool {
		return order[findings[i].Severity] < order[findings[j].Severity]
	})
}

// staleForeshadowThreshold 根据总章节数计算伏笔停滞阈值。
func staleForeshadowThreshold(completedChapters int) int {
	t := completedChapters / 3
	if t < ThresholdForeshadowMin {
		return ThresholdForeshadowMin
	}
	return t
}
