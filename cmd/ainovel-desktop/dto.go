package main

import (
	"github.com/voocel/ainovel-cli/internal/host"
)

// eventDTO 是投影给前端的事件形状。host.Event 的时间字段是 time.Time，
// 直接 EventsEmit 会被 JSON 编成 RFC3339 字符串，前端解析即可。这里显式拍平成
// 字段，一是把 time.Duration（纳秒 int64）转成毫秒方便前端展示，二是把 Running()
// 的判定固化下来，前端不必重算“开始/结束共用 ID”的合并语义。
type eventDTO struct {
	ID         string `json:"id"`
	Time       string `json:"time"`       // RFC3339，首次发出时间
	FinishedAt string `json:"finishedAt"` // 空串 = 进行中
	Failed     bool   `json:"failed"`
	Running    bool   `json:"running"`
	Category   string `json:"category"`
	Agent      string `json:"agent"`
	Summary    string `json:"summary"`
	Kind       string `json:"kind"`
	Level      string `json:"level"`
	Depth      int    `json:"depth"`
	DurationMs int64  `json:"durationMs"`
	RetryAt    string `json:"retryAt"` // 空串 = 无重试倒计时
}

func toEventDTO(ev host.Event) eventDTO {
	dto := eventDTO{
		ID:         ev.ID,
		Time:       ev.Time.Format(rfc3339),
		Failed:     ev.Failed,
		Running:    ev.Running(),
		Category:   ev.Category,
		Agent:      ev.Agent,
		Summary:    ev.Summary,
		Kind:       ev.Kind,
		Level:      ev.Level,
		Depth:      ev.Depth,
		DurationMs: ev.Duration.Milliseconds(),
	}
	if !ev.FinishedAt.IsZero() {
		dto.FinishedAt = ev.FinishedAt.Format(rfc3339)
	}
	if !ev.RetryAt.IsZero() {
		dto.RetryAt = ev.RetryAt.Format(rfc3339)
	}
	return dto
}

const rfc3339 = "2006-01-02T15:04:05.000Z07:00"
