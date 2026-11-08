package domain

import "slices"

// TimelineEvent 时间线事件。
type TimelineEvent struct {
	Chapter    int      `json:"chapter"`
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Characters []string `json:"characters,omitempty"`
}

// ForeshadowEntry 伏笔条目。
type ForeshadowEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	PlantedAt   int    `json:"planted_at"`
	Status      string `json:"status"` // planted / advanced / resolved
	ResolvedAt  int    `json:"resolved_at,omitempty"`
}

// ForeshadowUpdate 伏笔增量操作。
type ForeshadowUpdate struct {
	ID          string `json:"id"`
	Action      string `json:"action"` // plant / advance / resolve
	Description string `json:"description,omitempty"`
}

// RestoreOwnPlants 把旧记录里本章种下、而新记录未再声明的伏笔 plant 补回队首。
// 一章埋过哪些伏笔是它自身的历史事实，重写正文不改变这一点；丢掉它，章节记录
// 全量重放时本章及后续章节的 advance/resolve 会找不到前置 plant，整条链报错。
func RestoreOwnPlants(prev, next []ForeshadowUpdate) []ForeshadowUpdate {
	declared := make(map[string]struct{}, len(next))
	for _, u := range next {
		if u.Action == "plant" {
			declared[u.ID] = struct{}{}
		}
	}
	var restored []ForeshadowUpdate
	for _, u := range prev {
		if u.Action != "plant" {
			continue
		}
		if _, ok := declared[u.ID]; ok {
			continue
		}
		declared[u.ID] = struct{}{}
		restored = append(restored, u)
	}
	if len(restored) == 0 {
		return next
	}
	// plant 必须排在同章 advance/resolve 之前，重放才能先建起条目。
	return append(restored, next...)
}

// RelationshipEntry 人物关系条目。
type RelationshipEntry struct {
	CharacterA string `json:"character_a"`
	CharacterB string `json:"character_b"`
	Relation   string `json:"relation"`
	Chapter    int    `json:"chapter"`
}

// ConsistencyIssue 一致性问题。
type ConsistencyIssue struct {
	Type           string `json:"type"`     // 模型依据 rubric 给出的具体问题维度
	Severity       string `json:"severity"` // critical / error / warning
	Description    string `json:"description"`
	Evidence       string `json:"evidence,omitempty"` // 证据：原文片段、具体情节或状态数据
	Suggestion     string `json:"suggestion,omitempty"`
	Chapters       []int  `json:"chapters,omitempty"` // 证据实际落在哪些章节
	RequiresChange bool   `json:"requires_change"`    // 是否应立即进入返工队列，由 Editor 语义判断
}

// DimensionScore 单维度评审评分。
type DimensionScore struct {
	Dimension string `json:"dimension"`         // 由评审 rubric 定义，可按任务扩展
	Score     int    `json:"score"`             // 0-100
	Verdict   string `json:"verdict,omitempty"` // 兼容旧审阅；运行时不再用阈值覆盖模型判断
	Comment   string `json:"comment,omitempty"` // 该维度的简要结论
}

// ReviewEntry Editor 的审阅条目。
type ReviewEntry struct {
	Chapter          int                `json:"chapter"`
	Scope            string             `json:"scope"` // chapter / global / arc
	Issues           []ConsistencyIssue `json:"issues"`
	Dimensions       []DimensionScore   `json:"dimensions,omitempty"`      // 分维度评分
	ContractStatus   string             `json:"contract_status,omitempty"` // met / partial / missed
	ContractMisses   []string           `json:"contract_misses,omitempty"` // 未达成的 contract 条目
	ContractNotes    string             `json:"contract_notes,omitempty"`  // 对 contract 履行情况的简述
	Verdict          string             `json:"verdict"`                   // accept / polish / rewrite
	Summary          string             `json:"summary"`
	AffectedChapters []int              `json:"affected_chapters,omitempty"` // 需要重写/打磨的章节号
	Recheck          *ReviewRecheck     `json:"recheck,omitempty"`           // 正文返工后的一次定向复核；不改写原评审
}

// ReviewRecheck 只记录本次评审引起的正文改动。Result 落盘后不再自动复审或入队。
type ReviewRecheck struct {
	Chapters []int                `json:"chapters"`
	Result   *ReviewRecheckResult `json:"result,omitempty"`
}

type ReviewRecheckResult struct {
	Summary          string                  `json:"summary"`
	UnresolvedIssues []UnresolvedReviewIssue `json:"unresolved_issues"`
}

type UnresolvedReviewIssue struct {
	Issue    int    `json:"issue"` // 原评审 issues 中的序号，从 1 开始
	Evidence string `json:"evidence"`
}

// RecheckIssue 判断原问题是否属于这次正文返工的复核范围。
func (r *ReviewEntry) RecheckIssue(number int) bool {
	if r.Recheck == nil || number < 1 || number > len(r.Issues) {
		return false
	}
	issue := r.Issues[number-1]
	if !issue.RequiresChange {
		return false
	}
	for _, ch := range issue.Chapters {
		if slices.Contains(r.Recheck.Chapters, ch) {
			return true
		}
	}
	return false
}

// CriticalCount 返回 critical 级别问题数量。
func (r *ReviewEntry) CriticalCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "critical" {
			n++
		}
	}
	return n
}

// ErrorCount 返回 error 级别问题数量。
func (r *ReviewEntry) ErrorCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			n++
		}
	}
	return n
}

// Dimension 返回指定维度的评分；不存在则返回 nil。
func (r *ReviewEntry) Dimension(name string) *DimensionScore {
	if r == nil {
		return nil
	}
	for i := range r.Dimensions {
		if r.Dimensions[i].Dimension == name {
			return &r.Dimensions[i]
		}
	}
	return nil
}
