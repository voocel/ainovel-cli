package action

import "github.com/voocel/ainovel-cli/internal/domain"

type Kind string

const (
	KindEmitNotice         Kind = "emit_notice"
	KindFollowUp           Kind = "follow_up"
	KindSetFlow            Kind = "set_flow"
	KindSetPendingRewrites Kind = "set_pending_rewrites"
	KindCompleteRewrite    Kind = "complete_rewrite"
	KindClearHandledSteer  Kind = "clear_handled_steer"
	KindSaveCheckpoint     Kind = "save_checkpoint"
	KindSaveHandoff        Kind = "save_handoff"
	KindMarkComplete       Kind = "mark_complete"
)

type Action struct {
	Kind     Kind
	DedupKey string
	Category string
	Summary  string
	Level    string
	Message  string
	Flow     domain.FlowState
	Chapters []int
	Reason   string
	Chapter  int
	Label    string
}

func EmitNotice(category, summary, level string) Action {
	return Action{Kind: KindEmitNotice, Category: category, Summary: summary, Level: level}
}

func WithDedupKey(action Action, key string) Action {
	action.DedupKey = key
	return action
}

func FollowUp(message string) Action {
	return Action{Kind: KindFollowUp, Message: message}
}

func SetFlow(flow domain.FlowState) Action {
	return Action{Kind: KindSetFlow, Flow: flow}
}

func SetPendingRewrites(chapters []int, reason string) Action {
	return Action{Kind: KindSetPendingRewrites, Chapters: chapters, Reason: reason}
}

func CompleteRewrite(chapter int) Action {
	return Action{Kind: KindCompleteRewrite, Chapter: chapter}
}

func ClearHandledSteerAction() Action {
	return Action{Kind: KindClearHandledSteer}
}

func SaveCheckpointAction(label string) Action {
	return Action{Kind: KindSaveCheckpoint, Label: label}
}

func SaveHandoffAction(label string) Action {
	return Action{Kind: KindSaveHandoff, Label: label}
}

func MarkComplete() Action {
	return Action{Kind: KindMarkComplete}
}
