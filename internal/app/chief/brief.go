package chief

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// brief 是附在用户每句话前的书况（D74）：宿主此刻的记录，确定性渲染，故事语言。它在
// 会话的动态尾部，不破坏稳定前缀的缓存。
func (s *Session) brief(ctx context.Context, view View) (string, error) {
	snap, err := s.service.bench.WorkbenchSnapshot(ctx, s.projectID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	writing, paused := s.writing, s.paused
	var since time.Time // 用户上一句话的时刻：之后的里程碑是主编还不知道的
	for _, entry := range s.entries {
		if entry.Kind == EntryUser {
			since = entry.At
		}
	}
	s.mu.Unlock()
	lines := []string{
		"【书况】宿主此刻的记录，不是用户说的话。",
		"作品：" + oneLine(snap.Intent.Premise, 80),
		"进度：" + progress(snap),
		"创作：" + situation(snap, writing, paused),
	}
	if recent := milestonesSince(snap.Milestones, since); recent != "" {
		label := "上一回合以来："
		if since.IsZero() {
			label = "最近的进展："
		}
		lines = append(lines, label+recent)
	}
	if change := pendingChange(snap); change != "" {
		lines = append(lines, "待用户确认的修改："+change)
	}
	if decision := pendingDecision(snap); decision != "" {
		lines = append(lines, "待用户决定："+decision)
	}
	if marked := markedChapters(snap.Marks); marked != "" {
		lines = append(lines, "标记在案："+marked)
	}
	looking := "全书"
	if view.Chapter > 0 {
		looking = chapterLabel(snap, view.Chapter)
	}
	lines = append(lines, "用户正在看："+looking, fmt.Sprintf("生效的要求：%d 条", len(snap.Directives)))
	return strings.Join(lines, "\n"), nil
}

func progress(snap workbench.WorkbenchSnapshot) string {
	written, planned := 0, 0
	for _, node := range snap.Outline {
		if node.Node.Kind != model.PlanChapter {
			continue
		}
		planned++
		if node.State == workbench.ChapterConfirmed {
			written++
		}
	}
	length := snap.Length
	text := fmt.Sprintf("已入稿 %d 章，已规划 %d 章", written, planned)
	switch {
	case length.Fixed > 0:
		return text + fmt.Sprintf("；篇幅由用户固定为 %d 章", length.Fixed)
	case length.Final > 0:
		return text + fmt.Sprintf("；篇幅交给 AI，已承诺 %d 章收官", length.Final)
	case length.Compass != nil && length.Compass.ScaleMax > 0:
		return text + fmt.Sprintf("；篇幅交给 AI，预计 %d 章", length.Compass.ScaleMax)
	default:
		return text + "；篇幅交给 AI"
	}
}

// situation 是创作此刻的状态。按键暂停的书在主编这一回合结束后接着写，要照实说：主编以为
// 书停着，就会先问用户、不先暂停，回合一结束就照旧的样子写下去（D87）。
func situation(snap workbench.WorkbenchSnapshot, writing, paused bool) string {
	if writing {
		if snap.CurrentPhase != "" {
			return "正在推进（" + snap.CurrentPhase + "）"
		}
		return "正在推进"
	}
	if snap.Run == nil {
		return "还没开始"
	}
	switch snap.Run.State {
	case model.RunPaused:
		if paused {
			return "用户按键暂停，你这一回合结束后接着写"
		}
		return "已暂停"
	case model.RunWaitingUser:
		return "停下等用户决定"
	case model.RunCompleted:
		return "全书已完成"
	case model.RunFailed:
		return "出了问题停下：" + snap.Run.StateReason
	case model.RunCancelled:
		return "已停止"
	default:
		return "停着，可以继续"
	}
}

// pendingChange 是主编提出、等用户确认的修改与用户能怎么做；没有为空。
func pendingChange(snap workbench.WorkbenchSnapshot) string {
	change := snap.Change
	if change == nil {
		return ""
	}
	text := oneLine(change.Proposal.Reason, 80)
	switch {
	case change.Stale:
		return text + "。之后作品又变了，不能照它确认：按用户的意思重新 propose_change 替换它，或 revert_change 撤回"
	case len(change.Options) > 0:
		return text + fmt.Sprintf("。与已写章节有出入（%d 处），用户在确认卡上按序号选处置；详情用 book_history 看，不要了就 revert_change 撤回", len(change.Findings))
	}
	return text + "。用户按 y 确认；不要了就 revert_change 撤回"
}

// markedChapters 是标记在案的章与用户能怎么做（D94）；没有为空。
func markedChapters(marks []novel.Mark) string {
	const limit = 12
	if len(marks) == 0 {
		return ""
	}
	numbers := make([]string, 0, min(len(marks), limit))
	for _, mark := range marks[:min(len(marks), limit)] {
		numbers = append(numbers, strconv.Itoa(mark.Number))
	}
	text := "第 " + strings.Join(numbers, "、") + " 章"
	if len(marks) > limit {
		text += fmt.Sprintf("等 %d 章", len(marks))
	}
	return text + "自动修订用尽仍有审阅意见，创作没停（意见用 book_reviews 看）。用户说就这样，请他选中那一章按 y 接受；" +
		"要改就问清楚怎么改，用 note_requirement 记下并设 rework"
}

// pendingDecision 是等用户决定的事与用户能怎么做；没有为空。
func pendingDecision(snap workbench.WorkbenchSnapshot) string {
	decision := snap.Decision
	switch {
	case decision == nil:
		return ""
	case decision.Held != "":
		return oneLine(decision.Reason, 120) + "。用户按 y 接受现状；要改就问清楚怎么改，用 note_requirement 记下并设 rework，再 resume_creation"
	case decision.HasProposal:
		return oneLine(decision.Reason, 120) + "。稿件用 book_draft 看；用户按 y 通过，不满意就说理由，用 reject_draft 退回"
	default:
		return oneLine(decision.Reason, 120)
	}
}

// milestonesSince 是 since 之后的生产里程碑，只列最近几件。
func milestonesSince(milestones []workbench.Milestone, since time.Time) string {
	const limit = 6
	var texts []string
	for _, milestone := range milestones {
		if milestone.At.After(since) {
			texts = append(texts, milestone.Text)
		}
	}
	if len(texts) > limit {
		texts = append([]string{fmt.Sprintf("（更早的 %d 件略）", len(texts)-limit)}, texts[len(texts)-limit:]...)
	}
	return strings.Join(texts, "；")
}

func chapterLabel(snap workbench.WorkbenchSnapshot, number int) string {
	for _, node := range snap.Outline {
		if node.Node.Kind == model.PlanChapter && node.Number == number {
			return fmt.Sprintf("第 %d 章《%s》", number, node.Node.Title)
		}
	}
	return fmt.Sprintf("第 %d 章", number)
}

func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
