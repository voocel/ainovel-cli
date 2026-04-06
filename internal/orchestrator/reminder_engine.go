package orchestrator

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/orchestrator/action"
	"github.com/voocel/ainovel-cli/internal/orchestrator/recovery"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const readOnlyReminderThreshold = 5

type toolProgress struct {
	Agent   string
	Tool    string
	Error   bool
	Message string
}

type reminderSnapshot struct {
	Progress  *domain.Progress
	RunMeta   *domain.RunMeta
	Store     *storepkg.Store
	Committed bool
}

type reminderEngine struct {
	store               *storepkg.Store
	consecutiveReadOnly int
	recentReadOnly      []string
	pending             []action.Action
}

var readOnlyTools = map[string]struct{}{
	"novel_context":     {},
	"read_chapter":      {},
	"check_consistency": {},
}

var productiveTools = map[string]struct{}{
	"save_foundation":     {},
	"plan_chapter":        {},
	"draft_chapter":       {},
	"commit_chapter":      {},
	"save_review":         {},
	"save_arc_summary":    {},
	"save_volume_summary": {},
}

func newReminderEngine(store *storepkg.Store) *reminderEngine {
	return &reminderEngine{store: store}
}

func (e *reminderEngine) observeSubAgentDone(store *storepkg.Store, committed bool) {
	if store == nil {
		return
	}
	progress, _ := store.Progress.Load()
	runMeta, _ := store.RunMeta.Load()
	snapshot := reminderSnapshot{
		Progress:  progress,
		RunMeta:   runMeta,
		Store:     store,
		Committed: committed,
	}

	if matched, ruleActions := foundationIncompleteReminderRule(snapshot); matched {
		e.enqueue(ruleActions...)
	}
	if matched, ruleActions := uncommittedDraftReminderRule(snapshot); matched {
		e.enqueue(ruleActions...)
	}
}

func (e *reminderEngine) observeToolProgress(progress toolProgress) {
	if progress.Tool == "" {
		return
	}
	if progress.Error {
		e.observeToolFailure(progress.Tool, progress.Message)
		return
	}
	if _, ok := readOnlyTools[progress.Tool]; ok {
		threshold := e.readOnlyThreshold()
		e.consecutiveReadOnly++
		e.recentReadOnly = append(e.recentReadOnly, progress.Tool)
		if len(e.recentReadOnly) > threshold {
			e.recentReadOnly = e.recentReadOnly[len(e.recentReadOnly)-threshold:]
		}
		if e.consecutiveReadOnly >= threshold {
			tools := strings.Join(uniqueStrings(e.recentReadOnly), " / ")
			e.resetReadOnlyStreak()
			e.enqueue(
				action.WithDedupKey(action.EmitNotice("SYSTEM", "连续只读探索过多，提醒开始落稿", "warn"), "reminder.readonly_spiral.notice"),
				action.WithDedupKey(action.FollowUp(fmt.Sprintf(
					"[系统] 你已经连续 %d 次调用只读工具（最近：%s）。停止继续探索，开始采取行动：如果信息已足够，请立即规划、落稿、提交，或明确给出需要重写/评审的决定。不要继续无止境地读取上下文。",
					threshold, tools)), "reminder.readonly_spiral.followup"),
			)
		}
		return
	}
	if _, ok := productiveTools[progress.Tool]; ok {
		e.resetReadOnlyStreak()
	}
}

func (e *reminderEngine) observeToolFailure(tool, detail string) {
	e.resetReadOnlyStreak()
	summary := tool + " 执行失败，提醒修正后再继续"
	message := fmt.Sprintf(
		"[系统] 工具 %s 执行失败。请先检查参数、前置状态和输入数据，再决定是否重试；不要在同一错误上盲目重复调用。",
		tool,
	)
	if detail != "" {
		message += "\n失败原因：" + truncateLog(detail, 120)
	}
	e.enqueue(
		action.WithDedupKey(action.EmitNotice("SYSTEM", summary, "warn"), "reminder.tool_failure."+tool+".notice"),
		action.WithDedupKey(action.FollowUp(message), "reminder.tool_failure."+tool+".followup"),
	)
}

func (e *reminderEngine) resetReadOnlyStreak() {
	e.consecutiveReadOnly = 0
	e.recentReadOnly = nil
}

func (e *reminderEngine) readOnlyThreshold() int {
	if e == nil || e.store == nil {
		return readOnlyReminderThreshold
	}
	progress, err := e.store.Progress.Load()
	if err != nil {
		return readOnlyReminderThreshold
	}
	policy := domain.NewChapterMemoryPolicy(progress, domain.NewContextProfile(progressTotalChapters(progress)), false)
	if policy.ReadOnlyThreshold > 0 {
		return policy.ReadOnlyThreshold
	}
	return readOnlyReminderThreshold
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (e *reminderEngine) enqueue(actions ...action.Action) {
	for _, act := range actions {
		if e.hasPending(act) {
			continue
		}
		e.pending = append(e.pending, act)
	}
}

func (e *reminderEngine) drain() []action.Action {
	if len(e.pending) == 0 {
		return nil
	}
	actions := append([]action.Action(nil), e.pending...)
	e.pending = nil
	return actions
}

func (e *reminderEngine) hasPending(target action.Action) bool {
	for _, existing := range e.pending {
		if samePolicyAction(existing, target) {
			return true
		}
	}
	return false
}

func samePolicyAction(a, b action.Action) bool {
	if a.DedupKey != "" || b.DedupKey != "" {
		return a.DedupKey != "" && a.DedupKey == b.DedupKey
	}
	return a.Kind == b.Kind &&
		a.Category == b.Category &&
		a.Summary == b.Summary &&
		a.Level == b.Level &&
		a.Message == b.Message &&
		a.Flow == b.Flow &&
		a.Reason == b.Reason &&
		a.Chapter == b.Chapter &&
		a.Label == b.Label &&
		strings.Join(intSliceToStrings(a.Chapters), ",") == strings.Join(intSliceToStrings(b.Chapters), ",")
}

func intSliceToStrings(values []int) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%d", value))
	}
	return out
}

func foundationIncompleteReminderRule(snapshot reminderSnapshot) (bool, []action.Action) {
	progress := snapshot.Progress
	if progress == nil || progress.Phase != domain.PhasePremise || snapshot.Store == nil {
		return false, nil
	}

	var missing []string
	if outline, _ := snapshot.Store.Outline.LoadOutline(); len(outline) == 0 {
		missing = append(missing, "outline")
	}
	if chars, _ := snapshot.Store.Characters.Load(); len(chars) == 0 {
		missing = append(missing, "characters")
	}
	if rules, _ := snapshot.Store.World.LoadWorldRules(); len(rules) == 0 {
		missing = append(missing, "world_rules")
	}
	if len(missing) == 0 {
		return false, nil
	}

	msg := fmt.Sprintf(
		"[系统] 基础设定不完整，以下项目尚未保存：%v。请重新调用对应规划师补全这些设定。在基础设定全部完备前，不要调用 writer。",
		missing,
	)
	if guidance := recovery.PlanningTierGuidance(snapshot.RunMeta); guidance != "" {
		msg += "\n" + guidance
	}
	return true, []action.Action{
		action.WithDedupKey(action.EmitNotice("SYSTEM", fmt.Sprintf("基础设定不完整，缺失: %v", missing), "warn"), "reminder.foundation_incomplete.notice"),
		action.WithDedupKey(action.FollowUp(msg), "reminder.foundation_incomplete.followup"),
	}
}

func uncommittedDraftReminderRule(snapshot reminderSnapshot) (bool, []action.Action) {
	progress := snapshot.Progress
	if snapshot.Committed || progress == nil || progress.Phase == domain.PhaseComplete || snapshot.Store == nil {
		return false, nil
	}

	chapter := 1
	if progress.InProgressChapter > 0 {
		chapter = progress.InProgressChapter
	} else if len(progress.CompletedChapters) > 0 {
		chapter = progress.NextChapter()
	}
	draft, _ := snapshot.Store.Drafts.LoadDraft(chapter)
	if draft == "" {
		return false, nil
	}

	return true, []action.Action{
		action.WithDedupKey(action.EmitNotice("SYSTEM", fmt.Sprintf("第 %d 章有草稿但未提交", chapter), "warn"), fmt.Sprintf("reminder.uncommitted_draft.ch%d.notice", chapter)),
		action.WithDedupKey(action.FollowUp(fmt.Sprintf(
			"[系统] Writer 结束但第 %d 章草稿未提交。请重新调用 writer 完成该章的自审和提交（commit_chapter）。",
			chapter)), fmt.Sprintf("reminder.uncommitted_draft.ch%d.followup", chapter)),
	}
}

func progressTotalChapters(progress *domain.Progress) int {
	if progress == nil {
		return 0
	}
	return progress.TotalChapters
}
