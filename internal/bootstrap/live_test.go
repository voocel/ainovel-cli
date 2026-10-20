package bootstrap_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/chief"
	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	resources "github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// 真实模型验收（§0.4，D37）：AINOVEL_LIVE 是数据库路径，设了才跑，用本机配置的模型。断言只管
// 结构，文学上的判断看日志与诊断：
//
//	AINOVEL_LIVE=/tmp/live.db go test ./internal/bootstrap -run TestLive -v -timeout 8h
//	go run ./cmd/ainovel-cli --headless --db /tmp/live.db diag --project <日志里的作品>
//
// AINOVEL_LIVE_PROJECT 给出库里已有的书时接着跑它（长篇超时后续跑）；AINOVEL_LIVE_CHAPTERS 换长篇的章数（默认 40，0 交给 AI）；AINOVEL_LIVE_RAW 存下工具参数坏掉的原始响应。
// 换模型、思考强度用 AINOVEL_MODEL、AINOVEL_THINKING（见 TestLiveModelTrial）。
const liveUserID = "live-user"

// internalID 是文档编号的样子（c4、f43、e16、b1）：给用户看的文字里不该出现（D66）。
var internalID = regexp.MustCompile(`(^|[^A-Za-z0-9])[a-z]{1,2}[0-9]+([^A-Za-z0-9]|$)`)

const livePremise = "雨城邮差陈渡在一封无人认领的信里发现一把铜钥匙，循着它去找失踪多年的姐姐，却撞破了邮局局长的秘密。"

// liveUser 是验收里的用户：经主编说话，按工作台的按键处理决定卡与确认卡。
type liveUser struct {
	t       *testing.T
	ctx     context.Context
	api     *bootstrap.App
	session *chief.Session
	project string
	start   time.Time
	seen    int    // 已记进日志的对话条目
	ended   int    // 已记进日志的推进
	retried uint64 // 已看过的实时活动条目
	cue     *liveCue
	watch   func(workbench.WorkbenchSnapshot) // 推进途中每次看现场时调用
	steps   []liveStep                        // 推进途中按入稿章数依次做的事
}

// liveCue 是写作途中插的一句话：现场走到 phase 时说。
type liveCue struct{ phase, words string }

// liveStep 是推进途中的一件事：入稿到第 at 章后做。
type liveStep struct {
	at   int
	name string
	do   func()
}

func openLive(t *testing.T) *liveUser {
	path := os.Getenv("AINOVEL_LIVE")
	if path == "" {
		t.Skip("AINOVEL_LIVE 未设：真实模型验收不跑")
	}
	if raw := os.Getenv("AINOVEL_LIVE_RAW"); raw != "" {
		recordBrokenToolCalls(t, raw)
	}
	dir, err := appconfig.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := appconfig.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := appconfig.WithEnv(saved)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authorityStore.Close() })
	api := bootstrap.New(authorityStore, bootstrap.Options{Version: "live", ConfigDir: t.TempDir()})
	if err := api.Models.Apply(saved, config); err != nil {
		t.Fatal(err)
	}
	project := os.Getenv("AINOVEL_LIVE_PROJECT") // 接着跑库里已有的书
	if project == "" {
		project = "live-" + time.Now().Format("20060102-150405")
	}
	session := api.Chief.Open(ctx, project, liveUserID)
	t.Cleanup(func() {
		session.Close()
		api.Chief.Wait(30 * time.Second)
	})
	t.Logf("作品 %s · 数据库 %s · 模型 %s", project, path, config.Model)
	return &liveUser{t: t, ctx: ctx, api: api, session: session, project: project, start: time.Now()}
}

func (u *liveUser) log(format string, args ...any) {
	u.t.Helper()
	u.t.Logf("[%s] %s", time.Since(u.start).Round(time.Second), fmt.Sprintf(format, args...))
}

func (u *liveUser) snapshot() workbench.WorkbenchSnapshot {
	u.t.Helper()
	snap, err := u.api.Workbench.WorkbenchSnapshot(u.ctx, u.project)
	if err != nil {
		u.t.Fatal(err)
	}
	return snap
}

// say 跟主编说一句话，等它回完，返回这一回合的条目。
func (u *liveUser) say(words string, chapter int) []chief.Entry {
	u.t.Helper()
	u.log("用户：%s", words)
	if err := u.session.Send(u.ctx, words, chief.View{Chapter: chapter}); err != nil {
		u.t.Fatal(err)
	}
	deadline := time.After(20 * time.Minute)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for u.session.Busy() {
		select {
		case <-u.session.Changes():
		case <-tick.C:
		case <-deadline:
			u.t.Fatal("主编 20 分钟没有回完")
		}
	}
	thread := u.session.Thread()
	turn := thread.Entries[u.seen:]
	for _, entry := range turn {
		switch {
		case entry.Kind == chief.EntryChief:
			u.log("主编：%s", entry.Text)
		case entry.Kind == chief.EntryAction && entry.Failed:
			u.log("  ✗ %s", entry.Text)
		case entry.Kind == chief.EntryAction:
			u.log("  · %s", entry.Text)
		}
	}
	u.seen = len(thread.Entries)
	if thread.Err != "" {
		u.t.Errorf("主编这一回合出错：%s", thread.Err)
	}
	return turn
}

// used 报告这一回合里主编是否成功用了某个工具。
func used(turn []chief.Entry, tool string) bool {
	return slices.ContainsFunc(turn, func(entry chief.Entry) bool { return entry.Tool == tool && entry.Done && !entry.Failed })
}

// chiefWrites 是改动作品或创作的主编工具：用户只是提问时一个都不该碰。
var chiefWrites = []string{"note_requirement", "retire_requirement", "reject_draft", "set_length",
	"pause_creation", "resume_creation", "propose_change", "revert_change"}

// ask 问一句、不要求改什么：主编只读和答，要求与待确认的修改都不变。
func (u *liveUser) ask(words string, chapter int) {
	u.t.Helper()
	before := u.snapshot()
	for _, entry := range u.say(words, chapter) {
		if slices.Contains(chiefWrites, entry.Tool) {
			u.t.Errorf("只是提问，主编却动了手（%s）：%s", entry.Tool, words)
		}
	}
	if after := u.snapshot(); len(after.Directives) != len(before.Directives) || after.Change != nil && before.Change == nil {
		u.t.Errorf("只是提问，书的要求或修改变了：%s", words)
	}
}

// pauseAndResume 让主编停下，停着时做 meanwhile（说的话不带着续写），再让它继续。
func (u *liveUser) pauseAndResume(meanwhile func()) {
	u.t.Helper()
	if turn := u.say("先停一下，我想想后面怎么走。", 0); !used(turn, "pause_creation") || u.session.Production().Writing {
		u.t.Errorf("主编没有停下创作")
	}
	meanwhile()
	if u.session.Production().Writing {
		u.t.Errorf("停着时说的话让创作接着写了")
	}
	if turn := u.say("想好了，继续写吧。", 0); !used(turn, "resume_creation") || !u.session.Production().Writing {
		u.t.Errorf("主编没有接着写")
	}
}

// retire 让主编退掉一条要求：之后生效的要求里没有它。
func (u *liveUser) retire(words string, match func(model.Directive) bool) {
	u.t.Helper()
	if turn := u.say(words, 0); !used(turn, "retire_requirement") || liveDirective(u.snapshot(), match) != nil {
		u.t.Errorf("主编没有退掉要求：%s", words)
	}
}

// retitleAndUndo 让主编改第 number 章的标题，再让它撤销：标题回到原样。
func (u *liveUser) retitleAndUndo(number int, title string) {
	u.t.Helper()
	original := titleAt(u.snapshot(), number)
	u.say(fmt.Sprintf("把第 %d 章的标题改成《%s》。", number, title), 0)
	u.settleChange()
	if got := titleAt(u.snapshot(), number); got != title {
		u.t.Errorf("第 %d 章的标题没改成《%s》：《%s》", number, title, got)
	}
	if turn := u.say("算了，刚才改标题那个撤销吧。", 0); !used(turn, "revert_change") {
		u.t.Errorf("主编没有撤销修改")
	}
	u.settleChange()
	if got := titleAt(u.snapshot(), number); got != original {
		u.t.Errorf("撤销后第 %d 章的标题是《%s》，原来是《%s》", number, got, original)
	}
}

// settleChange 像守着工作台的用户：有等确认的修改就按 y。改标题不该与已写的章有出入，
// 真出了就记错、选第一种处置接着跑。
func (u *liveUser) settleChange() {
	u.t.Helper()
	snap := u.snapshot()
	switch change := snap.Change; {
	case change == nil || change.Stale:
	case len(change.Options) > 0:
		u.t.Errorf("意外的确认卡：%v", change.Findings)
		if err := u.session.Resolve(u.ctx, change.Proposal.ID, change.Options[0].Strategy); err != nil {
			u.t.Fatal(err)
		}
	default:
		u.confirm(snap)
	}
}

// reworkOnly 在完本的书上点名返工第 number 章（D78）：记成那一章的返工要求，开新一轮，
// 只重写那一章。
func (u *liveUser) reworkOnly(number int, words string) workbench.WorkbenchSnapshot {
	u.t.Helper()
	snap := u.snapshot()
	first := snap.Run.ID
	before := make(map[int]string)
	for _, chapter := range snap.Manuscript {
		before[chapter.Number] = liveText(chapter)
	}
	u.say(words, number)
	snap = u.snapshot()
	u.directives(snap)
	if rework := liveDirective(snap, func(d model.Directive) bool { return d.Rework }); rework == nil || rework.Scope != model.DirectiveScopeChapters(number, number) {
		u.t.Errorf("D78：没有记成第 %d 章的返工要求：%+v", number, rework)
	}
	snap = u.advance(completed)
	if snap.Run.ID == first {
		u.t.Errorf("D78：完本后的返工没有开新一轮")
	}
	for _, chapter := range snap.Manuscript {
		if changed := liveText(chapter) != before[chapter.Number]; changed != (chapter.Number == number) {
			u.t.Errorf("D78：第 %d 章改过=%t，只该重写第 %d 章", chapter.Number, changed, number)
		}
	}
	return snap
}

// titleAt 是第 number 章的规划标题。
func titleAt(snap workbench.WorkbenchSnapshot, number int) string {
	for _, entry := range snap.Outline {
		if entry.Node.Kind == model.PlanChapter && entry.Number == number {
			return entry.Node.Title
		}
	}
	return ""
}

// idle 等推进与主编都停下；途中按 cue 插话。现场 90 分钟没有变化算卡住。
func (u *liveUser) idle() {
	u.t.Helper()
	deadline := time.Now().Add(90 * time.Minute)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	phase := ""
	for {
		production := u.session.Production()
		if !production.Writing && !u.session.Busy() {
			if production.Ended > u.ended {
				u.ended = production.Ended
				end := production.Last
				u.log("推进停下：%s %s %v", end.Result.RunState, end.Result.RunReason, end.Err)
			}
			return
		}
		if time.Now().After(deadline) {
			u.t.Fatal("现场 90 分钟没有变化")
		}
		select {
		case <-u.session.Changes():
		case <-tick.C:
			snap := u.snapshot()
			if snap.CurrentPhase != phase {
				phase = snap.CurrentPhase
				deadline = time.Now().Add(90 * time.Minute)
				u.log("现场：%s", phase)
			}
			u.retries()
			if u.watch != nil {
				u.watch(snap)
			}
			if len(u.steps) > 0 && len(snap.Manuscript) >= u.steps[0].at {
				step := u.steps[0]
				u.steps = u.steps[1:]
				u.log("—— 入稿 %d 章：%s", len(snap.Manuscript), step.name)
				step.do()
			}
			if snap.Change != nil && !snap.Change.Stale && len(snap.Change.Options) == 0 {
				u.confirm(snap) // 守着工作台的用户看到确认卡就按 y，不等创作停下
			}
			if u.cue != nil && strings.Contains(phase, u.cue.phase) {
				cue := u.cue
				u.cue = nil
				u.say(cue.words, 0)
			}
		}
	}
}

// retries 把重试记进日志：模型调用断了重发、任务重开只出现在实时活动里，不落库。
func (u *liveUser) retries() {
	feed, ok := u.api.Workbench.RunActivity(u.project)
	if !ok {
		return
	}
	for _, entry := range feed.Entries {
		if entry.ID <= u.retried {
			continue
		}
		u.retried = entry.ID
		switch {
		case entry.Kind == activity.Retry:
			u.log("重试第 %d 次：%s", entry.Attempt, entry.Err)
		case entry.Kind == activity.Notice && entry.Tone == activity.ToneRetry:
			u.log("%s", entry.Text)
		}
	}
}

// advance 像用户守着工作台一样推进，直到 stop：稿件按 y 通过，主编的修改按 y 确认，修订
// 预算用尽的章接受现状，停着就回车继续。
func (u *liveUser) advance(stop func(workbench.WorkbenchSnapshot) bool) workbench.WorkbenchSnapshot {
	u.t.Helper()
	stuck := ""
	for range 200 {
		u.idle()
		snap := u.snapshot()
		if stop(snap) {
			return snap
		}
		if snap.Run != nil {
			at := fmt.Sprintf("%s %s %s", snap.Run.State, snap.Run.StateReason, snap.Run.WaitingOperationID)
			if at == stuck {
				u.t.Fatalf("回车继续后原地不动：%s", at)
			}
			stuck = ""
			if snap.Change == nil && (snap.Decision == nil || snap.Decision.Stale || !snap.Decision.HasProposal && snap.Decision.Held == "") {
				stuck = at // 下面会回车继续
			}
		}
		switch {
		case snap.Change != nil && (snap.Change.Stale || len(snap.Change.Options) > 0):
			u.t.Fatalf("意外的确认卡：%+v", snap.Change)
		case snap.Change != nil:
			u.confirm(snap)
		case snap.Decision != nil && snap.Decision.Held != "":
			u.accept(snap)
		case snap.Decision != nil && snap.Decision.HasProposal && !snap.Decision.Stale:
			u.approve(snap)
		case snap.Run == nil || snap.Run.State == model.RunCompleted || snap.Run.State == model.RunCancelled:
			u.t.Fatalf("推进停在意外的地方：%+v", snap.Run)
		case snap.Run.State == model.RunFailed:
			u.t.Fatalf("创作失败：%s", snap.Run.StateReason)
		default:
			u.log("回车继续（%s：%s）", snap.Run.State, snap.Run.StateReason)
			u.session.Write(chief.WriteParams{Chapters: novelapp.KeepChapters})
		}
	}
	u.t.Fatal("推进 200 步还没到")
	return workbench.WorkbenchSnapshot{}
}

func (u *liveUser) confirm(snap workbench.WorkbenchSnapshot) {
	u.t.Helper()
	u.log("y 确认主编的修改：%s", snap.Change.Proposal.Reason)
	if err := u.session.Confirm(u.ctx, snap.Change.Proposal.ID); err != nil {
		u.t.Fatal(err)
	}
}

func (u *liveUser) approve(snap workbench.WorkbenchSnapshot) {
	u.t.Helper()
	what := "规划方案"
	if number := draftOf(snap); number > 0 {
		what = fmt.Sprintf("第 %d 章", number)
	}
	u.log("y 通过%s（%s）", what, snap.Decision.Reason)
	if _, err := u.api.Decisions.Approve(u.ctx, snap.Decision.Proposal.ID, liveUserID, time.Now().UTC()); err != nil {
		u.t.Fatal(err)
	}
	u.session.Write(chief.WriteParams{Chapters: novelapp.KeepChapters})
}

// accept 是修订预算用尽时按 y 接受现状：阻塞意见一次入账为用户裁决（D43）。
func (u *liveUser) accept(snap workbench.WorkbenchSnapshot) {
	u.t.Helper()
	u.log("y 接受现状：%s", snap.Decision.Reason)
	var held []string
	for _, finding := range snap.Findings {
		if finding.ChapterID == snap.Decision.Held && finding.Severity == model.FindingBlocking {
			u.log("  接受的阻塞意见：%s", finding.Note)
			held = append(held, finding.ID)
		}
	}
	now := time.Now().UTC()
	if _, err := u.api.Reviews.AddAdjudication(u.ctx, novelapp.AddAdjudicationCommand{
		ProjectID: u.project, ChangeID: projectdoc.NewID("adjudication", now), UserID: liveUserID,
		Findings: held, Reason: "接受当前版本", CreatedAt: now,
	}); err != nil {
		u.t.Fatal(err)
	}
	u.session.Write(chief.WriteParams{Chapters: novelapp.KeepChapters})
}

// draftOf 是等用户决定的章节稿的章号；不是章节稿为 0。
func draftOf(snap workbench.WorkbenchSnapshot) int {
	if snap.Decision == nil || !snap.Decision.HasProposal {
		return 0
	}
	for _, candidate := range snap.Candidates {
		if candidate.OperationID == snap.Decision.Proposal.OperationID {
			return candidate.Chapter.Number
		}
	}
	return 0
}

func completed(snap workbench.WorkbenchSnapshot) bool {
	return snap.Run != nil && snap.Run.State == model.RunCompleted
}

func liveDirective(snap workbench.WorkbenchSnapshot, match func(model.Directive) bool) *model.Directive {
	for _, directive := range snap.Directives {
		if match(directive.Directive) {
			return &directive.Directive
		}
	}
	return nil
}

func liveChapter(snap workbench.WorkbenchSnapshot, number int) model.ManuscriptChapter {
	for _, chapter := range snap.Manuscript {
		if chapter.Number == number {
			return chapter
		}
	}
	return model.ManuscriptChapter{}
}

func liveText(chapter model.ManuscriptChapter) string {
	var text strings.Builder
	for _, block := range chapter.Blocks {
		text.WriteString(block.Text)
	}
	return text.String()
}

func (u *liveUser) directives(snap workbench.WorkbenchSnapshot) {
	for _, directive := range snap.Directives {
		u.log("  要求 %s ·「%s」· rework=%t · source=%s · %+v", directive.Scope, directive.Text, directive.Rework, directive.Source, directive.Constraints)
	}
}

// 一本 4 章、逐章确认的书：开书（S1）；规划等确认时提字数与第 2 章的要求（S13、S11）；退回
// 第 1 章并说明原因（S10）；写第 2 章途中插话（S12）；完本后点名返工第 3 章（D78）。
func TestLiveChiefAcceptance(t *testing.T) {
	u := openLive(t)

	u.log("S1：一句话开书，4 章，逐章确认")
	u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: 4, Approval: model.ApprovalManual})
	u.advance(func(snap workbench.WorkbenchSnapshot) bool { return snap.Decision != nil && snap.Decision.HasProposal })

	u.log("S13、S11：规划等确认时提字数与第 2 章的要求")
	u.say("每章两千字左右。第 2 章陈渡要吃一次大亏，别让他赢。", 0)
	snap := u.snapshot()
	u.directives(snap)
	if words := liveDirective(snap, func(d model.Directive) bool { return d.Constraints != nil }); words == nil ||
		words.Scope != model.DirectiveScopeProject || words.Constraints.TargetWords != 2000 {
		t.Errorf("S13：字数要求没有记成全书每章 2000 字：%+v", words)
	}
	setback := liveDirective(snap, func(d model.Directive) bool { return d.Scope == model.DirectiveScopeChapters(2, 2) })
	if setback == nil || setback.Source == "" || !strings.Contains(setback.Text, "大亏") {
		t.Errorf("S11：第 2 章的要求没有按原话记在第 2 章：%+v", setback)
	}

	u.log("S10：第 1 章稿件等确认时退回并说明原因")
	u.advance(func(snap workbench.WorkbenchSnapshot) bool { return draftOf(snap) == 1 })
	turn := u.say("第 1 章不行：开头铺垫太长，直接从陈渡拆开那封信写起。", 1)
	snap = u.snapshot()
	u.directives(snap)
	if !used(turn, "reject_draft") {
		t.Errorf("S10：主编没有退回稿件")
	}
	if rejection := liveDirective(snap, func(d model.Directive) bool { return strings.Contains(d.Text, "开头铺垫太长") }); rejection == nil || rejection.Source == "" {
		t.Errorf("S10：退稿理由没有按原话入账：%+v", rejection)
	}

	u.log("S12：写第 2 章途中插话；之后逐章通过到完本")
	u.cue = &liveCue{phase: "落笔第 2 章", words: "接下来让邮局局长露个面，先别揭穿他。"}
	snap = u.advance(completed)
	u.directives(snap)
	if u.cue != nil {
		t.Errorf("S12：没赶上第 2 章的写作途中")
	}
	// “接下来”可以是正在写的这一章，也可以是从这一章起：都不该落到已写的章上。
	if villain := liveDirective(snap, func(d model.Directive) bool { return strings.Contains(d.Text, "邮局局长") }); villain == nil ||
		villain.Source == "" || villain.Covers(model.DirectiveTarget{ChapterNumber: 1}) {
		t.Errorf("S12：插话没有记成正在写的章及之后的要求：%+v", villain)
	}
	if len(snap.Manuscript) != 4 {
		t.Fatalf("完本应有 4 章：%d", len(snap.Manuscript))
	}
	for _, chapter := range snap.Manuscript {
		u.log("第 %d 章《%s》%d 字", chapter.Number, outlineTitle(snap, chapter.PlanNodeID), chapter.Runes())
		if n := chapter.Runes(); n < 1800 || n > 2200 {
			t.Errorf("S13：第 %d 章 %d 字，超出两千字的 ±10%%", chapter.Number, n)
		}
	}
	if setback != nil {
		requirement := novelapp.DirectiveRequirementID(setback.ID)
		for _, r := range snap.Briefs[2].Requirements {
			if r.ID == requirement && r.Status != model.CheckSatisfied {
				t.Errorf("S11：第 2 章的要求审阅结论是 %q", r.Status)
			}
		}
	}

	u.log("D78：完本后点名返工第 3 章")
	u.reworkOnly(3, "第 3 章太拖了，紧凑点重写。")
}

// 主编的对话面（短）：只提问时不动书；停下创作，停着时记一条要求又退掉，再继续；完本后
// 改一章标题再撤销；最后把篇幅从 6 章改成 8 章接着写。短到可以连跑几遍看通过率。
func TestLiveChiefConversation(t *testing.T) {
	u := openLive(t)
	hook := func(d model.Directive) bool { return strings.Contains(d.Text, "钩子") }
	u.steps = []liveStep{
		{1, "只提问", func() { u.ask("第 1 章写得怎么样？哪里还能更好？", 1) }},
		{2, "停下、记一条要求又退掉、继续", func() {
			u.pauseAndResume(func() {
				if turn := u.say("以后每章结尾都留个钩子。", 0); !used(turn, "note_requirement") || liveDirective(u.snapshot(), hook) == nil {
					t.Errorf("没有记下留钩子的要求")
				}
				u.retire("算了，留钩子那条要求不要了。", hook)
			})
		}},
	}
	u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: 6, Approval: model.ApprovalAuto})
	u.advance(completed)
	if len(u.steps) > 0 {
		t.Fatalf("没赶上的步骤：%s", u.steps[0].name)
	}

	u.log("完本后改标题再撤销")
	u.retitleAndUndo(2, "雨夜")
	u.log("篇幅改成 8 章")
	if turn := u.say("篇幅改成 8 章，接着写。", 0); !used(turn, "set_length") {
		t.Errorf("主编没有调篇幅")
	}
	if snap := u.advance(completed); len(snap.Manuscript) != 8 {
		t.Errorf("改成 8 章后写了 %d 章", len(snap.Manuscript))
	}
}

// 主编的对话面（长篇）：40 章自动推进，沿途照常跟主编说话——问刚写的、问很早的、问后面
// 怎么走都不动书；给后面的章节区间记要求、过了一阵再退掉；点名第 30 章要发生的事；改标题
// 再撤销；停下再继续；完本后点名返工第 6 章。看书大了以后主编还找不找得准，对话越积越长时
// 的成本与会话压缩（库里的对话记录）。
func TestLiveChiefLongBook(t *testing.T) {
	const chapters = 40
	u := openLive(t)
	slow := func(d model.Directive) bool { return strings.Contains(d.Text, "节奏放慢") }
	crack := func(d model.Directive) bool { return strings.Contains(d.Text, "破绽") }
	u.steps = []liveStep{
		{3, "问刚写的", func() { u.ask("第 3 章写得怎么样？哪里还能更好？", 3) }},
		{8, "给后面的区间记要求", func() {
			u.say("第 20 到 25 章节奏放慢一点，多写写雨城的街巷。", 0)
			if d := liveDirective(u.snapshot(), slow); d == nil || d.Scope != model.DirectiveScopeChapters(20, 25) {
				t.Errorf("没有记成第 20–25 章的要求：%+v", d)
			}
		}},
		{12, "点名第 30 章要发生的事", func() {
			turn := u.say("第 30 章让局长第一次露出破绽。", 0)
			if d := liveDirective(u.snapshot(), crack); !used(turn, "propose_change") && (d == nil || !d.Covers(model.DirectiveTarget{ChapterNumber: 30})) {
				t.Errorf("第 30 章的事既没记成要求也没改进规划：%+v", d)
			}
		}},
		{16, "问很早的", func() { u.ask("第 5 章里发生了什么？我有点记不清了。", 0) }},
		{21, "退掉区间要求", func() { u.retire("第 20 到 25 章放慢节奏那条要求不要了。", slow) }},
		{26, "改标题再撤销", func() { u.retitleAndUndo(12, "旧账") }},
		{31, "问后面怎么走", func() { u.ask("到现在主线推进得怎么样？接下来打算怎么走？", 0) }},
		{35, "停下再继续", func() { u.pauseAndResume(func() {}) }},
	}
	u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: chapters, Approval: model.ApprovalAuto})
	u.book(chapters, workbench.WorkbenchSnapshot{})
	if len(u.steps) > 0 {
		t.Fatalf("没赶上的步骤：%s", u.steps[0].name)
	}
	snap := u.snapshot()
	if d := liveDirective(snap, crack); d != nil {
		for _, r := range snap.Briefs[30].Requirements {
			if r.ID == novelapp.DirectiveRequirementID(d.ID) {
				u.log("第 30 章的要求「%s」：%s", r.Text, r.Status)
				if r.Status != model.CheckSatisfied {
					t.Errorf("第 30 章的要求没有兑现：%s", r.Status)
				}
			}
		}
	}

	u.log("D78：完本后点名返工第 6 章")
	snap = u.reworkOnly(6, "第 6 章太拖了，紧凑点重写。")
	chiefUsage := snap.Usage.Roles[model.ChiefRole]
	u.log("主编：输入 %dK（缓存 %dK）输出 %dK · 用时 %s", chiefUsage.Usage.Input/1000, chiefUsage.Usage.CacheRead/1000,
		chiefUsage.Usage.Output/1000, chiefUsage.Worked.Round(time.Second))
}

func outlineTitle(snap workbench.WorkbenchSnapshot, planID string) string {
	for _, entry := range snap.Outline {
		if entry.Node.ID == planID {
			return entry.Node.Title
		}
	}
	return ""
}

// S4：四章、自动推进的书写到第 2 章时停下，改一个已经写进正文的设定；有出入时选重写受影响
// 的章。主编吃不准改动的用意时会先问，验收里的用户答一句。后面没写的章纲点到它的，修改里
// 一并改（D87）；重写的章，章题与章纲随正文一起改。写完全书的大纲里不再有旧设定，重写的章
// 与之后写的章都用新设定。
func TestLiveSettingChange(t *testing.T) {
	u := openLive(t)
	u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: 4, Approval: model.ApprovalAuto})
	u.steps = []liveStep{{2, "停下改设定", func() {
		if err := u.session.Pause(u.ctx); err != nil {
			t.Fatal(err)
		}
	}}}
	snap := u.advance(func(snap workbench.WorkbenchSnapshot) bool {
		return snap.Run != nil && snap.Run.State == model.RunPaused
	})
	written := make(map[int]string)
	for _, chapter := range snap.Manuscript {
		written[chapter.Number] = liveText(chapter)
	}
	u.outline(snap, "改设定前")
	u.say("改个设定：信里那把铜钥匙其实是一枚旧邮票。", 0)
	if snap = u.snapshot(); snap.Change == nil {
		u.say("它只是姐姐留下的信物和线索，开锁的事你看着改。", 0) // 主编吃不准用法时会先问
		snap = u.snapshot()
	}
	var rewrite []int
	switch change := snap.Change; {
	case change == nil:
		t.Fatalf("S4：主编没有提出修改")
	case len(change.Options) == 0:
		t.Errorf("S4：语义分析没有发现与已写章节的出入：%+v", change.View)
		u.confirm(snap)
	default:
		card := strings.Join(change.View.Changes, "\n")
		for _, line := range change.View.Changes {
			u.log("  改动：%s", line)
		}
		for _, finding := range change.Findings {
			u.log("  出入：%s", finding)
			card += "\n" + finding
		}
		for i, option := range change.Options {
			u.log("  %d %s %v：%s", i+1, option.Strategy, option.Chapters, option.Explanation)
			card += "\n" + option.Explanation
			if option.Strategy == model.ResolutionRewriteAffected {
				rewrite = option.Chapters
			}
		}
		if id := internalID.FindString(card); id != "" {
			t.Errorf("S4：确认卡露出了内部编号 %q", id)
		}
		u.log("选 1 重写受影响的章")
		if err := u.session.Resolve(u.ctx, change.Proposal.ID, model.ResolutionRewriteAffected); err != nil {
			t.Fatal(err)
		}
	}
	snap = u.advance(completed)
	u.outline(snap, "写完后")
	for _, entry := range snap.Outline {
		if strings.Contains(entry.Node.Title+entry.Node.Summary, "铜钥匙") {
			t.Errorf("S4：大纲里还写着铜钥匙：%s（%s）", entry.Node.Title, entry.Node.Summary)
		}
	}
	for _, chapter := range snap.Manuscript {
		text := liveText(chapter)
		before, existed := written[chapter.Number]
		u.log("第 %d 章：改过 %t · 铜钥匙 %d 处 · 邮票 %d 处", chapter.Number, text != before, strings.Count(text, "铜钥匙"), strings.Count(text, "邮票"))
		switch {
		case slices.Contains(rewrite, chapter.Number) && (text == before || !strings.Contains(text, "邮票")):
			t.Errorf("S4：第 %d 章没有按改过的设定重写", chapter.Number)
		case !existed && strings.Contains(text, "铜钥匙"):
			t.Errorf("S4：改设定之后写的第 %d 章还在写铜钥匙", chapter.Number)
		}
	}
}

// outline 把大纲逐节记进日志：卷、故事弧与章的标题和梗概。
func (u *liveUser) outline(snap workbench.WorkbenchSnapshot, when string) {
	u.log("%s的大纲：", when)
	for _, entry := range snap.Outline {
		label := string(entry.Node.Kind)
		if entry.Number > 0 {
			label = fmt.Sprintf("第 %d 章", entry.Number)
		}
		u.log("  %s《%s》%s", label, entry.Node.Title, entry.Node.Summary)
	}
}

// planSummary 是第 number 章的规划梗概。
func planSummary(snap workbench.WorkbenchSnapshot, number int) string {
	for _, entry := range snap.Outline {
		if entry.Node.Kind == model.PlanChapter && entry.Number == number {
			return entry.Node.Summary
		}
	}
	return ""
}

// S2、S5：完善设定开书（受众、必须、禁止、结局方向），蓝图等确认时给出自己的前两章大纲、
// 后两章交给 AI；开写后锁住主角人设与第 4 章。写完看用户的大纲落进了那两章的规划、锁定
// 生效且 AI 没动锁住的内容、要求都核验通过。
func TestLiveDetailedSetup(t *testing.T) {
	u := openLive(t)
	intent := model.Intent{
		Premise: livePremise, Audience: "喜欢悬疑的成年读者", Required: []string{"每章结尾留悬念"},
		Forbidden: []string{"超自然力量"}, EndingDirection: "姐弟重逢，局长伏法",
	}
	u.log("S2：完善设定开书，4 章，里程碑确认")
	u.session.Write(chief.WriteParams{Intent: &intent, Chapters: 4, Approval: model.ApprovalMilestone})
	u.advance(func(snap workbench.WorkbenchSnapshot) bool { return snap.Decision != nil && snap.Decision.HasProposal })

	u.log("S2：蓝图等确认时给前两章的大纲（与常见开局不同，蓝图得按它重拟）")
	const outline = "大纲前两章按我的来：第 1 章从局长的视角写，他在雨夜烧掉一封信；第 2 章陈渡才出场，在退信里发现姐姐的信。后两章你来安排。"
	turn := u.say(outline, 0)
	u.log("退回蓝图：%v", used(turn, "reject_draft"))
	u.cue = &liveCue{phase: "落笔第 1 章", words: "陈渡的人设锁死：沉默寡言，左手有旧伤，别改。第 4 章也锁住，别让 AI 动。"}
	var locked string // 锁定时第 4 章的规划
	u.watch = func(snap workbench.WorkbenchSnapshot) {
		if locked == "" && u.cue == nil && liveOwned(u, model.DocumentPlan) {
			locked = planSummary(snap, 4)
		}
	}
	snap := u.advance(completed)
	if u.cue != nil {
		t.Fatal("S5：没赶上第 1 章的写作途中")
	}
	u.directives(snap)
	for number := 1; number <= len(snap.Manuscript); number++ {
		u.log("第 %d 章《%s》%d 字：%s", number, outlineTitle(snap, liveChapter(snap, number).PlanNodeID), liveChapter(snap, number).Runes(), planSummary(snap, number))
	}
	project, err := u.api.Projects.Project(u.ctx, u.project, model.InitialRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range project.Ownership {
		u.log("  锁定 %s %s %q", rule.Target.Key(), rule.Control, rule.Guidance)
	}
	// 人设落进了书里并锁住：陈渡名下有写着左手旧伤的设定，它受锁定保护。
	persona := slices.ContainsFunc(project.Canon, func(fact model.CanonFact) bool {
		owner := slices.IndexFunc(project.Entities, func(entity model.Entity) bool { return entity.ID == fact.SubjectID })
		return owner >= 0 && project.Entities[owner].Name == "陈渡" && strings.Contains(string(fact.Value), "左手") &&
			slices.ContainsFunc(project.Ownership, func(rule model.OwnershipRule) bool {
				return rule.Target == model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID} && rule.Control != model.ControlOpen
			})
	})
	if !persona || !liveOwned(u, model.DocumentPlan) {
		t.Errorf("S5：主角人设（%v）或第 4 章没有锁定", persona)
	}
	if locked != "" && planSummary(snap, 4) != locked {
		t.Errorf("S5：锁定后第 4 章的规划被改了：%s → %s", locked, planSummary(snap, 4))
	}
	// 烧信可能写成“焚信”“丢进炉膛”，章题与梗概合起来看。
	if plan := outlineTitle(snap, liveChapter(snap, 1).PlanNodeID) + planSummary(snap, 1); !strings.ContainsAny(plan, "烧焚") {
		t.Errorf("S2：第 1 章的规划没有按用户的大纲：%s", plan)
	}
	for number, brief := range snap.Briefs {
		for _, requirement := range brief.Requirements {
			if requirement.Status == model.CheckViolated || strings.Contains(outline, requirement.Text) && requirement.Status != model.CheckSatisfied {
				t.Errorf("S2：第 %d 章的要求「%s」：%s", number, requirement.Text, requirement.Status)
			}
		}
	}
}

// liveOwned 报告作品里有没有 AI 不能自由改的某类文档。
func liveOwned(u *liveUser, kind model.DocumentKind) bool {
	project, err := u.api.Projects.Project(u.ctx, u.project, model.InitialRevision)
	if err != nil {
		u.t.Fatal(err)
	}
	return slices.ContainsFunc(project.Ownership, func(rule model.OwnershipRule) bool {
		return rule.Target.Kind == kind && rule.Control != model.ControlOpen
	})
}

// S9、S3：自动推进写第 2 章时把审批调严（之后每章等用户过目）；第一份等确认的稿件到来时，改第
// 4 章的结局（未来计划），已经通过的章不动，第 4 章按新的写。
func TestLiveMidCourseControl(t *testing.T) {
	u := openLive(t)
	u.log("S9：自动推进，写第 2 章时调严审批")
	u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: 4, Approval: model.ApprovalAuto})
	u.cue = &liveCue{phase: "落笔第 2 章", words: "从现在起每章写完先给我看，我确认了再往下写。"}
	snap := u.advance(func(snap workbench.WorkbenchSnapshot) bool { return draftOf(snap) > 0 })
	if u.cue != nil {
		t.Fatal("S9：没赶上第 2 章的写作途中")
	}
	if snap.Approval != model.ApprovalManual {
		t.Errorf("S9：审批没有调成逐章确认：%s", snap.Approval)
	}
	held := draftOf(snap)
	u.log("第一份等确认的稿件：第 %d 章", held)

	u.log("S3：改第 4 章的结局")
	accepted := make(map[int]string)
	for _, chapter := range snap.Manuscript {
		accepted[chapter.Number] = liveText(chapter)
	}
	before := planSummary(snap, 4)
	u.say("第 4 章的结局改一下：姐姐没有回来，只托人带回一封信。", 0)
	snap = u.advance(completed)
	after := planSummary(snap, 4)
	u.log("第 4 章梗概：%s → %s", before, after)
	if after == before || !strings.Contains(after, "信") {
		t.Errorf("S3：第 4 章的规划没有按新结局改")
	}
	for number, text := range accepted {
		if liveText(liveChapter(snap, number)) != text {
			t.Errorf("S3：已经通过的第 %d 章被改了", number)
		}
	}
	if !strings.Contains(liveText(liveChapter(snap, 4)), "信") {
		t.Errorf("S3：第 4 章正文没有按新结局写")
	}
}

// 长篇验收（§0.4 #3）：自动推进写 40 章，记每章摊到的用量与用时；单章成本不该随章数线性增长。
func TestLiveLongBook(t *testing.T) {
	chapters := 40
	if value := os.Getenv("AINOVEL_LIVE_CHAPTERS"); value != "" {
		var err error
		if chapters, err = strconv.Atoi(value); err != nil || chapters != 0 && chapters < 20 {
			t.Fatalf("AINOVEL_LIVE_CHAPTERS 要给不少于 20 的章数，0 交给 AI：%q", value)
		}
	}
	u := openLive(t)
	u.log("长篇：一句话开书，%d 章（0 为交给 AI），自动推进", chapters)
	var start workbench.WorkbenchSnapshot
	if os.Getenv("AINOVEL_LIVE_PROJECT") != "" {
		start = u.snapshot() // 接着跑：已写的章不计入
	} else {
		u.session.Write(chief.WriteParams{Premise: livePremise, Chapters: chapters, Approval: model.ApprovalAuto})
	}
	perChapter := u.book(chapters, start)
	if len(perChapter) < 20 {
		return // 接着跑的书只看到后半程，首末段对照见库里的执行记录
	}
	// 每章摊到的输入按十章一段取平均：末段明显高于首段就是上下文随章数膨胀。
	decile := func(from int) int {
		sum := 0
		for _, usage := range perChapter[from : from+10] {
			sum += usage.Input
		}
		return sum / 10
	}
	first, final := decile(0), decile(len(perChapter)-10)
	u.log("每章摊到的输入：前十章 %dK，后十章 %dK", first/1000, final/1000)
	if final > 2*first {
		t.Errorf("单章输入随章数膨胀：前十章 %dK，后十章 %dK", first/1000, final/1000)
	}
}

// 写到很后面才改主角的设定（D77）：影响分析要对照受牵连的已写章。在已写了很多章的书上
// （AINOVEL_LIVE_PROJECT，用库副本）只提修改、不确认，看影响分析读多少、多久判完、给不给得出处置。
func TestLiveLateChange(t *testing.T) {
	if os.Getenv("AINOVEL_LIVE_PROJECT") == "" {
		t.Skip("AINOVEL_LIVE_PROJECT 未设：要在已写了很多章的书上跑")
	}
	u := openLive(t)
	before := u.snapshot()
	u.log("晚期改设定：已写 %d 章", len(before.Manuscript))
	started := time.Now()
	u.say("陈渡的设定改一下：他从小是左撇子，写字、投信、开锁都用左手。", 0)
	u.log("主编这一回合用时 %s", time.Since(started).Round(time.Second))
	// 与已写章没有出入时修改按审批直接生效，有出入时等用户选处置：两种都取那份提案。
	var proposal model.Proposal
	if change := u.snapshot().Change; change != nil {
		proposal = change.Proposal
		u.log("等确认：出入 %d 处，处置 %d 种", len(change.Findings), len(change.Options))
		for _, finding := range change.Findings {
			u.log("  出入：%s", finding)
		}
		for _, option := range change.Options {
			u.log("  %s 第 %v 章：%s", option.Strategy, option.Chapters, option.Explanation)
		}
	} else {
		history, err := u.api.Decisions.History(u.ctx, u.project, 1)
		if err != nil || len(history) == 0 || history[0].Author != model.ChiefAuthor || history[0].NewRevision <= before.Revision {
			t.Fatalf("主编没有提出修改：%+v %v", history, err)
		}
		proposal = history[0].Proposal
		u.log("直接生效：%s", proposal.Impact.Semantic.Status)
	}
	// 设定改动要对照写到该主体的已写章（model.SemanticScope），影响分析把它们的全文一并读。
	project, err := u.api.Projects.Project(u.ctx, u.project, proposal.BaseRevision)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []model.DocumentRef
	for _, patch := range proposal.Patches {
		var fact model.CanonFact
		if patch.Document.Kind == model.DocumentCanon && json.Unmarshal(patch.Content, &fact) == nil {
			subjects = append(subjects, model.DocumentRef{Kind: model.DocumentEntity, ID: fact.SubjectID})
		}
	}
	chapters, runes := 0, 0
	for _, chapter := range project.Manuscript {
		if slices.ContainsFunc(subjects, func(subject model.DocumentRef) bool { return slices.Contains(chapter.DependsOn, subject) }) {
			chapters, runes = chapters+1, runes+chapter.Runes()
		}
	}
	u.log("影响分析要对照 %d/%d 章、%d 字", chapters, len(project.Manuscript), runes)
}

// 换模型、思考强度对照：同一设定写 6 章，篇幅用官方写作标准的默认（软要求）；模型与档位用
// AINOVEL_MODEL、AINOVEL_THINKING 换，比每章用时、用量、字数与正文。
func TestLiveModelTrial(t *testing.T) {
	const chapters = 6
	u := openLive(t)
	u.log("对照：%d 章，默认篇幅，自动推进", chapters)
	if _, err := u.api.Projects.CreateProject(u.ctx, projectdoc.CreateProjectCommand{
		ProjectID: u.project, ChangeID: u.project + ":create", UserID: liveUserID, Reason: "建立作品",
		Draft: projectdoc.ProjectDraft{Intent: model.Intent{Premise: livePremise}, Approval: model.ApprovalAuto}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	u.session.Write(chief.WriteParams{Chapters: chapters})
	u.book(chapters, workbench.WorkbenchSnapshot{})
}

// 对照 v0（§0.4 第 3、4 条）：同一设定与要求写 40 章、自动推进。v0 用日志里打出的同一段需求
// 跑 headless，比每章输入、完本、返工与要求满足。篇幅两边都是软要求：v1 在官方写作标准里，
// v0 写进需求原文。
func TestLiveVersusV0(t *testing.T) {
	const chapters = versusChapters
	intent := versusIntent(t)
	u := openLive(t)
	u.log("v0 的需求：%s全书 %d 章，每章三到四千字。要求：%s。不要写%s。", intent.Premise, chapters, intent.Required[0], intent.Forbidden[0])
	u.session.Write(chief.WriteParams{Intent: &intent, Chapters: chapters, Approval: model.ApprovalAuto})
	u.book(chapters, workbench.WorkbenchSnapshot{})
	snap := u.snapshot()
	for number := 1; number <= chapters; number++ {
		for _, requirement := range snap.Briefs[number].Requirements {
			if requirement.Status != model.CheckSatisfied {
				u.log("第 %d 章的要求「%s」：%s", number, requirement.Text, requirement.Status)
			}
		}
	}
}

const versusChapters = 40

// versusIntents 是与 v0 对照的需求，AINOVEL_VERSUS 选一个（D92 起三个题材）：p1 悬疑（默认）、
// p2 慢节奏的家族群像、p3 网文长篇的玄幻成长。v0 用日志里打出的同一段话作 --prompt-file。
var versusIntents = map[string]model.Intent{
	"p1": {Premise: livePremise, Required: []string{"第 5 章陈渡第一次正面见到邮局局长"}, Forbidden: []string{"感情线"}},
	"p2": {Premise: "江边小城的老茶馆传到第三代：外婆想把它守下去，母亲要卖掉它还债，外孙女辞了城里的工作回来，在旧账本里翻到一笔四十年前没还上的人情。",
		Required: []string{"第 6 章外孙女弄清那笔人情欠的是谁"}, Forbidden: []string{"穿越、重生或超自然"}},
	"p3": {Premise: "被判为废脉的少年林晚能听见兵器的声音，他进铸剑宗当杂役，想查清父亲当年为什么铸出一把弑主之剑。",
		Required: []string{"第 8 章林晚第一次在人前用上听剑的本事"}, Forbidden: []string{"后宫"}},
}

func versusIntent(t *testing.T) model.Intent {
	intent, ok := versusIntents[cmp.Or(os.Getenv("AINOVEL_VERSUS"), "p1")]
	if !ok {
		t.Fatal("AINOVEL_VERSUS 只认 p1、p2、p3")
	}
	return intent
}

// 只跑开篇规划（D91 的验证）：与 TestLiveVersusV0 同一需求，规划入库就暂停，章纲与长线留在
// 库里比对。
func TestLiveVersusV0Plan(t *testing.T) {
	intent := versusIntent(t)
	u := openLive(t)
	paused := false
	u.watch = func(snap workbench.WorkbenchSnapshot) {
		if len(snap.Threads) == 0 || paused {
			return
		}
		paused = true
		if err := u.session.Pause(u.ctx); err != nil {
			t.Fatal(err)
		}
	}
	u.session.Write(chief.WriteParams{Intent: &intent, Chapters: versusChapters, Approval: model.ApprovalAuto})
	snap := u.advance(func(snap workbench.WorkbenchSnapshot) bool { return len(snap.Threads) > 0 })
	for _, thread := range snap.Threads {
		u.log("长线「%s」：%s · 作答第 %d 章", thread.Name, thread.Question, thread.Answer)
	}
	for _, node := range snap.Outline {
		if node.Node.Kind == model.PlanChapter {
			u.log("第 %d 章《%s》%v：%s", node.Number, node.Node.Title, snap.Briefs[node.Number].Threads, node.Node.Summary)
		}
	}
}

// book 自动推进到完本：每入稿一章记它摊到的用量与用时，完本后记总量与各角色占比。start 是
// 接着跑时开始的现场，此前写的章不计入。
func (u *liveUser) book(chapters int, start workbench.WorkbenchSnapshot) []model.Usage {
	written, last := len(start.Manuscript), start.Usage
	perChapter := make([]model.Usage, 0, chapters)
	u.watch = func(snap workbench.WorkbenchSnapshot) {
		if len(snap.Manuscript) <= written {
			return
		}
		delta := snap.Usage.Total
		delta.Input -= last.Total.Input
		delta.Output -= last.Total.Output
		delta.CacheRead -= last.Total.CacheRead
		delta.Cost -= last.Total.Cost
		count := len(snap.Manuscript) - written
		for range count {
			perChapter = append(perChapter, model.Usage{
				Input: delta.Input / count, Output: delta.Output / count,
				CacheRead: delta.CacheRead / count, Cost: delta.Cost / float64(count),
			})
		}
		u.log("入稿到第 %d 章 · 输入 %dK（缓存 %dK）输出 %dK · $%.3f · 用时 %s",
			len(snap.Manuscript), delta.Input/1000, delta.CacheRead/1000, delta.Output/1000, delta.Cost,
			(snap.Usage.Worked - last.Worked).Round(time.Second))
		written, last = len(snap.Manuscript), snap.Usage
	}
	snap := u.advance(completed)
	u.watch(snap) // 最后几章可能在两次查看之间入稿

	runes := 0
	for _, chapter := range snap.Manuscript {
		runes += chapter.Runes()
	}
	total := snap.Usage.Total
	u.log("完本 %d 章 %d 字 · 输入 %dK（缓存 %dK）输出 %dK · $%.2f · 用时 %s",
		len(snap.Manuscript), runes, total.Input/1000, total.CacheRead/1000, total.Output/1000, total.Cost,
		snap.Usage.Worked.Round(time.Second))
	for role, usage := range snap.Usage.Roles {
		u.log("  %s：输入 %dK（%.0f%%）输出 %dK · $%.2f · 用时 %s", role, usage.Usage.Input/1000,
			100*float64(usage.Usage.Input)/float64(total.Input), usage.Usage.Output/1000, usage.Usage.Cost, usage.Worked.Round(time.Second))
	}
	if compass := snap.Length.Compass; chapters == 0 && compass != nil {
		u.log("篇幅交给 AI：预计至多 %d 章，承诺 %d 章收官", compass.ScaleMax, compass.Final)
	}
	if chapters > 0 && len(snap.Manuscript) != chapters {
		u.t.Fatalf("写了 %d 章，要的是 %d 章", len(snap.Manuscript), chapters)
	}
	return perChapter
}

// S6、S7、S8：装一个创作包（写手用第一人称；审阅标准改为每章以问句收尾），配错的包装不上；
// 书级规则要求每章出现钟楼。写完两章看生效的 Prompt 带着包与规则的来源和版本、正文照着写；
// 用户把第 1 章删到每段只剩第一句，学出偏好候选，确认后进入用户的全局偏好。
func TestLiveCreatorAssets(t *testing.T) {
	u := openLive(t)
	now := func() time.Time { return time.Now().UTC() }
	pack := func(slot, text string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(dir+"/prompts", 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf(`{"id":"live-style","version":"1","name":"验收文风","prompts":{%q:"prompts/a.md","editor.story_review":"prompts/b.md"}}`, slot)
		for name, content := range map[string]string{
			"pack.jsonc": manifest, "prompts/a.md": text,
			"prompts/b.md": "审阅标准：每章最后一句必须是问句（以问号结尾），不是就记一条 blocking。",
		} {
			if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	const voice = "用第一人称写：叙述者就是主角陈渡，以“我”自称。"
	if _, err := u.api.Resources.InstallPackDirectory(u.ctx, pack("writer.chapter_drafts", voice), u.project+":bad-pack", liveUserID, "装错的包", now()); err == nil {
		t.Fatal("S8：写错槽位的包装上了")
	} else {
		u.log("S8：写错槽位的包装不上：%v", err)
	}
	installed, err := u.api.Resources.InstallPackDirectory(u.ctx, pack("writer.chapter_draft", voice), u.project+":pack", liveUserID, "装验收文风", now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.api.Projects.CreateProject(u.ctx, projectdoc.CreateProjectCommand{
		ProjectID: u.project, ChangeID: u.project + ":create", UserID: liveUserID, Reason: "建立作品",
		Draft: projectdoc.ProjectDraft{Intent: model.Intent{Premise: livePremise}, Approval: model.ApprovalAuto}, CreatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.api.Resources.SetProjectAssets(u.ctx, resources.SetProjectAssetsCommand{
		ProjectID: u.project, ChangeID: u.project + ":assets", UserID: liveUserID, Reason: "启用验收文风",
		Packs: []resources.PackRef{{ID: resources.OfficialPackID}, {ID: installed.Manifest.ID}}, CreatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	const rule = "每章都要出现一次雨城的钟楼。"
	if _, err := u.api.Resources.SetProjectOverlay(u.ctx, resources.SetProjectOverlayCommand{
		ProjectID: u.project, ChangeID: u.project + ":overlay", UserID: liveUserID, Rules: []string{rule}, Reason: "书级规则", CreatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}

	u.log("S6、S8：两章，自动推进")
	u.session.Write(chief.WriteParams{Chapters: 2, Approval: model.ApprovalAuto})
	snap := u.advance(completed)
	operation, err := u.api.Tasks.Operation(u.ctx, snap.Run.ID+":chapter:"+liveChapter(snap, 1).PlanNodeID)
	if err != nil {
		t.Fatal(err)
	}
	text, sources, err := u.api.Prompts.Prompt(u.ctx, operation.Snapshot.ConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		u.log("  来源 %s %s r%d %v", source.Layer, source.ID, source.Revision, source.Slots)
	}
	if !strings.Contains(text, voice) || !strings.Contains(text, rule) ||
		!slices.ContainsFunc(sources, func(source prompt.Source) bool {
			return source.ID == "live-style@1" && source.Revision == installed.Revision
		}) {
		t.Errorf("S6：写第 1 章生效的 Prompt 没有带上包与书级规则")
	}
	for _, chapter := range snap.Manuscript {
		body := liveText(chapter)
		last := strings.TrimRight(body, "”」』\"' \n")
		u.log("第 %d 章 %d 字 · “我” %d 次 · 钟楼 %v · 结尾「%s」", chapter.Number, chapter.Runes(),
			strings.Count(body, "我"), strings.Contains(body, "钟楼"), string([]rune(last)[max(0, len([]rune(last))-20):]))
		if strings.Count(body, "我") < 10 {
			t.Errorf("S8：第 %d 章不是第一人称", chapter.Number)
		}
		if !strings.Contains(body, "钟楼") {
			t.Errorf("S6：第 %d 章没有钟楼", chapter.Number)
		}
		if !strings.HasSuffix(last, "？") && !strings.HasSuffix(last, "?") {
			t.Errorf("S6：第 %d 章没有以问句收尾", chapter.Number)
		}
	}

	u.log("S7：用户把第 1 章删到每段只剩第一句")
	from := snap.Revision
	projection, err := u.api.Projects.ExportProject(u.ctx, u.project, from)
	if err != nil {
		t.Fatal(err)
	}
	for i := range projection.Manuscript {
		if projection.Manuscript[i].Number != 1 {
			continue
		}
		for j, block := range projection.Manuscript[i].Blocks {
			if end := strings.IndexAny(block.Text, "。！？"); end >= 0 {
				projection.Manuscript[i].Blocks[j].Text = block.Text[:end+len("。")]
			}
		}
	}
	proposal, err := u.api.Projects.ImportProject(u.ctx, u.project+":edit", liveUserID, "删繁就简", projection, now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.api.Decisions.Approve(u.ctx, proposal.ID, liveUserID, now()); err != nil {
		t.Fatal(err)
	}
	to := u.snapshot().Revision
	record, err := u.api.Resources.LearnPreference(u.ctx, resources.LearnPreferenceCommand{
		CandidateID: u.project + ":terse", ProfileID: liveUserID, Scope: "global", ProjectID: u.project,
		FromRevision: from, ToRevision: to, CreatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	u.log("偏好候选：%s · 依据 %q · 规则 %q", record.Candidate.Summary, record.Candidate.Evidence, record.Candidate.ProposedRules)
	if record.State != "pending" || len(record.Candidate.ProposedRules) == 0 {
		t.Fatalf("S7：候选 = %+v", record)
	}
	if _, err := u.api.Resources.ConfirmPreferenceCandidate(u.ctx, u.project+":confirm", liveUserID, "确认偏好",
		liveUserID, "global", record.Candidate.ID, now()); err != nil {
		t.Fatal(err)
	}
	profile, _, err := u.api.Resources.CreatorProfile(u.ctx, liveUserID, "global", 0)
	if err != nil || !slices.Contains(profile.ExplicitRules, record.Candidate.ProposedRules[0]) {
		t.Errorf("S7：确认后的全局偏好 = %+v, %v", profile, err)
	}
}
