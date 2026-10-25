package task

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	operationengine "github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
)

// 任务推进的现场旁白（页面设计 §3）：开工说这一步做什么、为什么是现在，收尾说落下了
// 什么——入稿多少字、审阅结论、等你确认、失败原因。文案由推导规则给（WorkReasons），
// 这里只补执行结果里的事实。

// ActivitySink 接收实时活动事件，由组合根注入；未装配时不发布。实现必须非阻塞。
type ActivitySink interface {
	Publish(activity.Event)
}

// SetActivitySink 装配实时活动通道；须在驱动开始前完成。
func (s *Manager) SetActivitySink(sink ActivitySink) { s.activity = sink }

func (s *Manager) announce(operation model.Operation, work creation.WorkItem) {
	if operation.Attempt > 0 {
		s.notify(operation, activity.ToneRetry, fmt.Sprintf("%s（第 %d 次尝试）", work.Reasons.Start, operation.Attempt+1))
		return
	}
	s.notify(operation, activity.ToneStep, work.Reasons.Start)
}

func (s *Manager) settle(work creation.WorkItem, result operationengine.RunResult) {
	operation := result.Operation
	switch operation.State {
	case model.OperationSucceeded:
		s.notify(operation, activity.ToneDone, work.Reasons.Done+outcomeDetail(result))
	case model.OperationAwaitingApproval:
		s.notify(operation, activity.ToneWait, work.Reasons.Waiting)
	case model.OperationFailed:
		// 原因按类别说（D76）；类别替掉了原文时，原文随旁白一起发出，活动里看得到具体出了什么错。
		text, raw := work.Reasons.Failure, ""
		if cause := operation.FailureCode.Explain(operation.Error); cause != "" {
			text += "：" + clip(cause, 80)
			if cause != operation.Error {
				raw = clip(operation.Error, 200)
			}
		}
		s.publish(operation, activity.ToneFail, text, raw)
	case model.OperationStale:
		s.notify(operation, activity.ToneInfo, "这一步依据的内容已经变了，改按最新内容重来")
	case model.OperationPaused:
		s.notify(operation, activity.ToneInfo, "已暂停，恢复后从这里接着做")
	case model.OperationCancelled:
		s.notify(operation, activity.ToneInfo, "这一步已取消")
	}
}

// outcomeDetail 是执行结果里值得一提的事实：入稿的字数、审阅的结论。
func outcomeDetail(result operationengine.RunResult) string {
	if len(result.Verdict) > 0 {
		var verdict model.ReviewVerdict
		if json.Unmarshal(result.Verdict, &verdict) != nil {
			return ""
		}
		if blocking := verdict.Blocking(); blocking > 0 {
			return fmt.Sprintf("：未通过 · %d 处未解决", blocking)
		}
		return "：通过"
	}
	if result.ChangeSet == nil {
		return ""
	}
	chapters, runes := 0, 0
	for _, patch := range result.ChangeSet.Patches {
		if patch.Document.Kind != model.DocumentManuscript || patch.Operation != model.PatchPut {
			continue
		}
		var chapter model.ManuscriptChapter
		if json.Unmarshal(patch.Content, &chapter) != nil {
			return ""
		}
		chapters, runes = chapters+1, runes+chapter.Runes()
	}
	switch chapters {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(" · %d 字", runes)
	default:
		return fmt.Sprintf(" · %d 章共 %d 字", chapters, runes)
	}
}

func (s *Manager) notify(operation model.Operation, tone activity.Tone, text string) {
	s.publish(operation, tone, text, "")
}

// publish 发出一条旁白；raw 是失败的原始错误，没有为空。
func (s *Manager) publish(operation model.Operation, tone activity.Tone, text, raw string) {
	if s.activity == nil {
		return
	}
	s.activity.Publish(activity.Event{
		ProjectID: operation.Target.ID, RunID: operation.RunID, OperationID: operation.ID,
		Kind: activity.Notice, Tone: tone, Text: text, Err: raw, At: time.Now().UTC(),
	})
}

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}
