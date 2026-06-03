package domain

// Persona 描述一个写作人格：源作者名 + LLM 生成的文风 block。
type Persona struct {
	Slug       string `json:"slug"`        // 稳定标识，用于工具路径与 agent 命名（如 wuzei）
	Author     string `json:"author"`      // 用户填写的作者名（如 乌贼）
	StyleBlock string `json:"style_block"` // LLM 生成的文风 prompt 片段
	Model      string `json:"model"`       // 生成该 block 的模型名（便于追溯）
	Fallback   bool   `json:"fallback"`    // true 表示 LLM 生成失败、用了通用兜底文案
}

// PersonaScore 是 Judge 对单个候选稿的评分。
type PersonaScore struct {
	Persona string  `json:"persona"`
	Score   float64 `json:"score"`
	Comment string  `json:"comment"`
}

// Verdict 是 Judge 对某一章 N 个候选稿的裁定结果。
type Verdict struct {
	Chapter       int            `json:"chapter"`
	Winner        string         `json:"winner"`         // 中选 persona slug
	Scores        []PersonaScore `json:"scores"`         // 各候选评分
	RevisionNotes string         `json:"revision_notes"` // 给中选 writer 的修改意见
	Promoted      bool           `json:"promoted"`       // 中选稿是否已提升为正式 draft.md（提升幂等标记）
}

// WinnerScore 返回中选 persona 的评分；找不到返回 0。
func (v Verdict) WinnerScore() float64 {
	for _, s := range v.Scores {
		if s.Persona == v.Winner {
			return s.Score
		}
	}
	return 0
}

// ContestProgress 记录某章候选生成的尝试与弃权状态（并发失败收敛用）。
type ContestProgress struct {
	Chapter   int            `json:"chapter"`
	Attempts  map[string]int `json:"attempts"`  // persona slug → 累计失败计数
	Abandoned []string       `json:"abandoned"` // 超阈值弃权的 persona slug（去重）
}
