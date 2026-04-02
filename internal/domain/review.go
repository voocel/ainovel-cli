package domain

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

// RelationshipEntry 人物关系条目。
type RelationshipEntry struct {
	CharacterA string `json:"character_a"`
	CharacterB string `json:"character_b"`
	Relation   string `json:"relation"`
	Chapter    int    `json:"chapter"`
}

// ConsistencyIssue 一致性问题。
type ConsistencyIssue struct {
	Type        string `json:"type"`     // consistency / character / pacing / continuity / foreshadow / hook / aesthetic
	Severity    string `json:"severity"` // critical / error / warning
	Description string `json:"description"`
	Evidence    string `json:"evidence,omitempty"` // 证据：原文片段、具体情节或状态数据
	Suggestion  string `json:"suggestion,omitempty"`
}

// DimensionScore 单维度评审评分。
type DimensionScore struct {
	Dimension string `json:"dimension"`         // consistency / character / pacing / continuity / foreshadow / hook / aesthetic
	Score     int    `json:"score"`             // 0-100
	Verdict   string `json:"verdict"`           // pass / warning / fail
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
