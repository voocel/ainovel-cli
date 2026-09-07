package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/app"
	"github.com/voocel/ainovel-cli/internal/service"
	"github.com/voocel/ainovel-cli/internal/store"
)

func newTestDeps(t *testing.T, configured bool) (Deps, *service.Service) {
	t.Helper()
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { authorityStore.Close() })
	api := service.New(authorityStore)
	return Deps{
		API: api, Configured: configured, ConfigDir: t.TempDir(), UserID: "tester",
		Rebuild: func(app.Config) (*service.Service, error) { return api, nil },
	}, api
}

func typeText(t *testing.T, m model, text string) model {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	return updated.(model)
}

func press(t *testing.T, m model, key tea.KeyType) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: key})
	return updated.(model), cmd
}

func pressRune(t *testing.T, m model, r rune) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(model), cmd
}

func pressTimes(t *testing.T, m model, key tea.KeyType, times int) model {
	t.Helper()
	for i := 0; i < times; i++ {
		m, _ = press(t, m, key)
	}
	return m
}

func TestWizardVerifiesThenSavesConfigAndEntersHome(t *testing.T) {
	deps, _ := newTestDeps(t, false)
	verified := false
	deps.Verify = func(context.Context, app.Config) error { verified = true; return nil }
	m := newModel(context.Background(), deps)
	if m.page != pageWizard {
		t.Fatalf("page = %v, want wizard when unconfigured", m.page)
	}
	for _, value := range []string{"deepseek", "deepseek-chat", "sk-test"} {
		m = typeText(t, m, value)
		m, _ = press(t, m, tea.KeyEnter)
	}
	m, cmd := press(t, m, tea.KeyEnter) // Base URL 可选，直接回车进入验证
	if !m.wizard.verifying || cmd == nil {
		t.Fatalf("wizard not verifying: %#v", m.wizard)
	}
	updated, _ := m.Update(findMsg[wizardVerifiedMsg](t, cmd))
	m = updated.(model)
	if !verified || m.page != pageHome {
		t.Fatalf("page = %v verified=%v (err %q), want home after verify", m.page, verified, m.wizard.err)
	}
	saved, err := app.LoadConfig(deps.ConfigDir)
	if err != nil || saved.Provider != "deepseek" || saved.Model != "deepseek-chat" {
		t.Fatalf("saved config = %#v, %v", saved, err)
	}
}

func TestWizardVerifyFailureLeavesConfigUnsaved(t *testing.T) {
	// 先验证再落盘（Codex 复审 #2）：连不上的配置绝不写进文件，不会锁死下次启动。
	deps, _ := newTestDeps(t, false)
	deps.Verify = func(context.Context, app.Config) error { return errors.New("api key invalid") }
	m := newModel(context.Background(), deps)
	for _, value := range []string{"bad-provider", "bad-model", "sk-test"} {
		m = typeText(t, m, value)
		m, _ = press(t, m, tea.KeyEnter)
	}
	m, cmd := press(t, m, tea.KeyEnter)
	updated, _ := m.Update(findMsg[wizardVerifiedMsg](t, cmd))
	m = updated.(model)
	if m.page != pageWizard || !strings.Contains(m.wizard.err, "连不上模型") {
		t.Fatalf("page=%v err=%q, want wizard with connectivity error", m.page, m.wizard.err)
	}
	if _, err := os.Stat(app.ConfigPath(deps.ConfigDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config file exists after failed verify: %v", err)
	}
}

func TestHomeRequiresPremiseBeforeCreating(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m, cmd := press(t, m, tea.KeyEnter)
	if m.page != pageHome || cmd != nil || m.home.err == "" {
		t.Fatalf("empty premise accepted: page=%v err=%q", m.page, m.home.err)
	}
}

func TestHomeCreateEntersWorkbenchAndSurfacesRunError(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "一个失忆的邮差替亡者送完最后一封信")
	m, cmd := press(t, m, tea.KeyEnter)
	if m.page != pageWorkbench || !m.bench.writing || cmd == nil {
		t.Fatalf("create: page=%v writing=%v", m.page, m.bench.writing)
	}
	if strings.Contains(m.bench.projectID, "book-") == false {
		t.Fatalf("project id = %q, want service-generated book id", m.bench.projectID)
	}
	// 执行异步命令：无执行器的服务会返回明确错误，工作台要把它呈现出来。
	message := findMsg[quickDoneMsg](t, cmd)
	updated, _ := m.Update(message)
	m = updated.(model)
	if m.bench.writing || m.bench.err == "" {
		t.Fatalf("run error not surfaced: writing=%v err=%q", m.bench.writing, m.bench.err)
	}
}

func TestHomeLibraryOpensExistingProject(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.Update(libraryLoadedMsg{entries: []libraryEntry{
		{id: "book-1", premise: "旧作", target: 3, written: 1, state: "等你决定"},
	}})
	m = updated.(model)
	m = pressTimes(t, m, tea.KeyTab, 6) // 章节数→自动化→完善设定→导入→配置→作品库
	if m.home.focus != focusLibrary {
		t.Fatalf("focus = %d, want library", m.home.focus)
	}
	m, cmd := press(t, m, tea.KeyEnter)
	if m.page != pageWorkbench || m.bench.projectID != "book-1" || cmd == nil {
		t.Fatalf("open: page=%v project=%q", m.page, m.bench.projectID)
	}
}

func TestStaleAsyncMessageFromPreviousBookIsDropped(t *testing.T) {
	// 异步结果不串台（Codex 复审 #1）：A 的创作结果迟到时，不得写进 B 的工作台。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "写书 A")
	m, _ = press(t, m, tea.KeyEnter)
	staleGen := m.bench.gen
	m, _ = press(t, m, tea.KeyEsc) // 创作中回首页
	updated, _ := m.openProject("book-b")
	m = updated.(model)
	if m.bench.gen == staleGen {
		t.Fatalf("generation not advanced: %d", m.bench.gen)
	}
	updated, _ = m.Update(quickDoneMsg{gen: staleGen, result: service.QuickWriteResult{
		RunState: domain.RunWaitingUser, RunReason: "A 的稿件等你确认",
	}})
	m = updated.(model)
	if m.bench.decision != nil || m.bench.err != "" || m.bench.projectID != "book-b" {
		t.Fatalf("stale message leaked into new bench: %#v", m.bench)
	}
}

func TestTypingOnPreselectedLibraryFocusesPremiseInput(t *testing.T) {
	// 作品库预选了上次作品时直接打字＝想写新书：首字符应聚焦一句话输入框并录入，
	// 而不是被库焦点吞掉（用户实际被卡住过）。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.Update(libraryLoadedMsg{entries: []libraryEntry{{id: "book-1", premise: "旧书"}}})
	m = updated.(model)
	m.home.focus = focusLibrary
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("写")})
	m = updated.(model)
	if m.home.focus != focusPremise || m.home.premise.Value() != "写" {
		t.Fatalf("focus=%d premise=%q, want premise input focused with first rune", m.home.focus, m.home.premise.Value())
	}
}

func TestLibraryDeleteRequiresDoubleConfirm(t *testing.T) {
	// 删除是不可恢复的危险操作：第一次 d 只出确认提示，按其他键取消；
	// 连按两次 d 才真正删除并刷新作品库。
	deps, api := newTestDeps(t, true)
	if _, err := api.CreateProject(context.Background(), service.CreateProjectCommand{
		ProjectID: "book-del", ChangeID: "create-del", UserID: deps.UserID,
		Reason: "删除测试", Draft: service.ProjectDraft{
			Intent: domain.Intent{Premise: "写废的书", TargetChapters: 1},
		},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	m := newModel(context.Background(), deps)
	updated, _ := m.Update(libraryLoadedMsg{entries: []libraryEntry{{id: "book-del", premise: "写废的书"}}})
	m = updated.(model)
	m.home.focus = focusLibrary

	m, _ = pressRune(t, m, 'd')
	if m.home.confirmDelete != "book-del" || !strings.Contains(m.home.notice, "再按一次 d") {
		t.Fatalf("first d: confirm=%q notice=%q", m.home.confirmDelete, m.home.notice)
	}
	m, _ = press(t, m, tea.KeyUp)
	if m.home.confirmDelete != "" || m.home.notice != "" {
		t.Fatalf("other key should cancel confirm: confirm=%q", m.home.confirmDelete)
	}

	m, _ = pressRune(t, m, 'd')
	m, cmd := pressRune(t, m, 'd')
	if m.home.confirmDelete != "" || cmd == nil {
		t.Fatalf("second d should fire delete: confirm=%q cmd=%v", m.home.confirmDelete, cmd)
	}
	message := findMsg[projectDeletedMsg](t, cmd)
	if message.err != nil || message.projectID != "book-del" {
		t.Fatalf("delete result = %#v", message)
	}
	if projects, err := api.ListProjects(context.Background()); err != nil || len(projects) != 0 {
		t.Fatalf("projects after delete = %#v, %v", projects, err)
	}
	updated, refreshCmd := m.Update(message)
	m = updated.(model)
	if m.home.notice != "已删除" || refreshCmd == nil {
		t.Fatalf("after delete: notice=%q refresh=%v", m.home.notice, refreshCmd)
	}
}

func TestStartupLandsOnWelcomeWithLastProjectPreselected(t *testing.T) {
	// 启动流程（用户定，2026-08-30）：配置完成一律落欢迎页；
	// 上次打开的作品在作品库预选，回车即恢复；打不开则回首页并说明原因。
	deps, _ := newTestDeps(t, true)
	if err := app.SaveState(deps.ConfigDir, app.State{LastProjectID: "book-2"}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	m := newModel(context.Background(), deps)
	if m.page != pageHome {
		t.Fatalf("startup page = %v, want welcome home", m.page)
	}
	updated, _ := m.Update(libraryLoadedMsg{entries: []libraryEntry{
		{id: "book-1", premise: "第一本"},
		{id: "book-2", premise: "上次写的"},
	}})
	m = updated.(model)
	if m.home.focus != focusLibrary || m.home.cursor != 1 {
		t.Fatalf("preselect: focus=%d cursor=%d, want library row 1", m.home.focus, m.home.cursor)
	}
	m, cmd := press(t, m, tea.KeyEnter)
	if m.page != pageWorkbench || m.bench.projectID != "book-2" || cmd == nil {
		t.Fatalf("open: page=%v project=%q", m.page, m.bench.projectID)
	}
	message := findMsg[benchRefreshedMsg](t, cmd)
	if message.err == nil {
		t.Fatal("refresh of missing project should fail")
	}
	updated, _ = m.Update(message)
	m = updated.(model)
	if m.page != pageHome || !strings.Contains(m.home.err, "打不开") {
		t.Fatalf("fallback: page=%v err=%q", m.page, m.home.err)
	}
}

func TestOpenWaitingProjectRestoresDecisionCard(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-1")
	m = updated.(model)
	gen := m.bench.gen
	waiting := domain.CreationRun{ID: "run:book-1", State: domain.RunWaitingUser, StateReason: "第 1 章等你确认"}
	updated, _ = m.Update(benchRefreshedMsg{
		gen: gen, snap: service.WorkbenchSnapshot{
			ProjectID: "book-1", Run: &waiting,
			Decision: &service.PendingDecision{
				Reason:   "第 1 章等你确认",
				Proposal: domain.Proposal{ID: "p-1", Reason: "第一章候选"}, HasProposal: true,
			},
		},
	})
	m = updated.(model)
	decision := m.bench.decision
	if decision == nil || !decision.hasProposal || !decision.continueAfter {
		t.Fatalf("decision card not restored: %#v", decision)
	}
	if !strings.Contains(m.View(), "等你决定") {
		t.Fatal("决定卡未出现在视图中")
	}
}

func TestDecisionRequiresExplicitApproveAndReasonRejects(t *testing.T) {
	// 页面设计 §2：批准必须由 y 明确确认；空回车不提交并回以指引；
	// n 进入修改意见输入，文字回车拒绝并按意见重写。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-1", 1)
	m.bench.loaded = true
	present := func() {
		m.bench.presentDecision(&decisionState{
			reason: "第 1 章写好了，等你确认", continueAfter: true,
			proposal: domain.Proposal{ID: "p-1", Reason: "第一章候选"}, hasProposal: true,
		})
	}
	present()
	if m.bench.input.Focused() {
		t.Fatal("待裁决时输入框不应自动聚焦（批准须 y 明确确认）")
	}
	if !strings.Contains(m.View(), "等你决定") {
		t.Fatal("决定卡未出现在视图中")
	}
	// 空回车：不提交，回以确认指引。
	m, cmd := press(t, m, tea.KeyEnter)
	if m.bench.decision == nil || cmd != nil || !strings.Contains(m.bench.notice, "按 y 确认通过") {
		t.Fatalf("空回车应回以指引且不提交：decision=%v notice=%q", m.bench.decision, m.bench.notice)
	}
	// y：明确批准。
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(model)
	if m.bench.decision != nil || cmd == nil {
		t.Fatal("y 未派发批准")
	}
	// n 聚焦输入，文字回车 = 拒绝重写。
	present()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(model)
	if !m.bench.input.Focused() {
		t.Fatal("n 未聚焦修改意见输入框")
	}
	m = typeText(t, m, "开头太平淡")
	m, cmd = press(t, m, tea.KeyEnter)
	if m.bench.decision != nil || m.bench.input.Value() != "" || cmd == nil {
		t.Fatalf("带意见回车未派发拒绝：decision=%v input=%q", m.bench.decision, m.bench.input.Value())
	}
	// 聚焦状态下空回车同样回以指引。
	present()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(model)
	m, cmd = press(t, m, tea.KeyEnter)
	if m.bench.decision == nil || cmd != nil || !strings.Contains(m.bench.notice, "按 y 确认通过") {
		t.Fatalf("聚焦空回车应回以指引：decision=%v notice=%q", m.bench.decision, m.bench.notice)
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.input.Focused() {
		t.Fatal("Esc 未释放输入框")
	}
}

func TestHomeRefineFormCreatesProjectWithFullIntent(t *testing.T) {
	deps, api := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "一个凡人进入修仙宗门")
	m = pressTimes(t, m, tea.KeyTab, 3) // 章节数→自动化→完善设定
	m, _ = press(t, m, tea.KeyEnter)
	if m.home.mode != homeForm {
		t.Fatalf("mode = %v, want form", m.home.mode)
	}
	m = typeText(t, m, "都市悬疑读者") // 受众
	m, _ = press(t, m, tea.KeyEnter)
	m = pressTimes(t, m, tea.KeyEnter, 3) // 期待体验/必须/禁止 留空
	m, cmd := press(t, m, tea.KeyEnter)   // 结局方向留空，最后一步开写
	if m.page != pageWorkbench || !m.bench.writing || cmd == nil {
		t.Fatalf("form create: page=%v writing=%v", m.page, m.bench.writing)
	}
	findMsg[quickDoneMsg](t, cmd) // 执行异步命令：CreateProject 成功，QuickWrite 因无执行器报错
	snapshot, err := api.Project(context.Background(), m.bench.projectID, domain.InitialRevision)
	if err != nil || snapshot.Intent.Audience != "都市悬疑读者" || snapshot.Intent.Premise == "" {
		t.Fatalf("project intent = %#v, %v", snapshot.Intent, err)
	}
}

func TestHomeImportEntryImportsProjectionAndApprovesViaDecisionCard(t *testing.T) {
	deps, api := newTestDeps(t, true)
	ctx := context.Background()
	origin, err := api.CreateProject(ctx, service.CreateProjectCommand{
		ProjectID: "origin-book", ChangeID: "create-origin", UserID: "tester", Reason: "导出源",
		Draft: service.ProjectDraft{
			Intent: domain.Intent{Premise: "旧书", TargetChapters: 1},
			Plan:   []domain.PlanNode{{ID: "v1", Kind: domain.PlanVolume, Title: "卷一", Summary: "起"}},
		},
		CreatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create origin: %v", err)
	}
	projection, err := api.ExportProject(ctx, origin.ID, origin.Revision)
	if err != nil {
		t.Fatalf("export origin: %v", err)
	}
	projection.ProjectID = "copied-book"
	payload, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("encode projection: %v", err)
	}
	path := filepath.Join(t.TempDir(), "book.jsonc")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write projection file: %v", err)
	}

	m := newModel(ctx, deps)
	m = pressTimes(t, m, tea.KeyTab, 4) // 章节数→自动化→完善设定→导入
	m, _ = press(t, m, tea.KeyEnter)
	if m.home.mode != homeImport {
		t.Fatalf("mode = %v, want import", m.home.mode)
	}
	m = typeText(t, m, path)
	m, cmd := press(t, m, tea.KeyEnter)
	updated, _ := m.Update(findMsg[importDoneMsg](t, cmd))
	m = updated.(model)
	decision := m.bench.decision
	if m.page != pageWorkbench || m.bench.projectID != "copied-book" ||
		decision == nil || !decision.hasProposal || decision.continueAfter {
		t.Fatalf("import landing: page=%v project=%q decision=%#v", m.page, m.bench.projectID, decision)
	}
	// 批准须 y 明确确认（页面设计 §2）。
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(model)
	updated, _ = m.Update(findMsg[decisionDoneMsg](t, cmd))
	m = updated.(model)
	copied, err := api.Project(ctx, "copied-book", domain.InitialRevision)
	if err != nil || len(copied.Plan) != 1 {
		t.Fatalf("copied book = %#v, %v", copied, err)
	}
}

func TestWorkbenchThreePaneOutlineDetailAndCandidateReading(t *testing.T) {
	// 页面设计 §2：宽屏三栏（大纲/主区/详情），窄屏降级单栏；
	// §3 三层校正链：待确认章节读候选稿并明确标注。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-1", 1)
	m.bench.loaded = true
	m.width, m.height = 180, 40
	run := domain.CreationRun{ID: "run:book-1", State: domain.RunWaitingUser}
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-1",
		Intent:    domain.Intent{Premise: "测试书", TargetChapters: 3, EndingDirection: "圆满"},
		Outline: []service.OutlineNode{
			{Node: domain.PlanNode{ID: "v1", Kind: domain.PlanVolume, Title: "卷一"}},
			{Node: domain.PlanNode{ID: "a1", Kind: domain.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, ParentID: "a1", Title: "第一章"}, Number: 1, State: service.ChapterConfirmed},
			{Node: domain.PlanNode{ID: "c2", Kind: domain.PlanChapter, ParentID: "a1", Title: "第二章"}, Number: 2, State: service.ChapterPending},
		},
		Manuscript: []domain.ManuscriptChapter{{
			ID: "chapter-1", PlanNodeID: "c1", Number: 1, Title: "第一章",
			Blocks: []domain.ManuscriptBlock{{ID: "b1", Text: "正文一"}},
		}},
		Candidates: []service.ChapterCandidate{{
			OperationID: "op-2", BaseRevision: 3,
			Chapter: domain.ManuscriptChapter{
				ID: "chapter-2", PlanNodeID: "c2", Number: 2, Title: "第二章",
				Blocks: []domain.ManuscriptBlock{{ID: "b2", Text: "候选正文二"}},
			},
		}},
		Canon: []domain.CanonFact{{
			ID: "f1", Kind: domain.CanonState, SubjectID: "hero", Predicate: "state.mood",
			Value: []byte(`"平静"`), SourceChapterID: "chapter-1",
		}},
		Findings: []domain.ReviewFinding{{ChapterID: "chapter-1", Severity: domain.FindingNote, Note: "伏笔呼应完整"}},
		Run:      &run,
	}
	// 快照刷新会把光标锚定在第一个章行（此处直接注入快照，手动对齐）。
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0)
	view := m.View()
	for _, want := range []string{"大纲", "卷一", "● 1", "◐ 2", "详情", "创作意图"} {
		if !strings.Contains(view, want) {
			t.Fatalf("three-pane view missing %q", want)
		}
	}
	// 选中第 2 章（待确认）回车 → 读候选稿并标注。
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.reading || !strings.Contains(m.bench.body.View(), "候选稿") {
		t.Fatalf("candidate reading: reading=%v", m.bench.reading)
	}
	m, _ = press(t, m, tea.KeyEsc)
	// 窄屏降级：单栏 Tab 视图仍在。
	m.width = 90
	if view := m.View(); !strings.Contains(view, "总览") {
		t.Fatal("narrow view should fall back to tabbed single pane")
	}
}

func TestOutlineFoldingCollapsesSubtreeAndAnchorsCursor(t *testing.T) {
	// M3 卷/弧折叠：回车在头行折叠/展开，折叠头行给出章数与 ◐ 摘要；
	// 折叠状态与光标身份都跨快照刷新存活；未规划占位行不可选。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-fold", 1)
	m.bench.loaded = true
	m.width, m.height = 180, 40
	outline := []service.OutlineNode{
		{Node: domain.PlanNode{ID: "v1", Kind: domain.PlanVolume, Title: "卷一"}},
		{Node: domain.PlanNode{ID: "a1", Kind: domain.PlanArc, ParentID: "v1", Title: "弧一"}},
		{Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, ParentID: "a1", Title: "第一章"}, Number: 1, State: service.ChapterConfirmed},
		{Node: domain.PlanNode{ID: "c2", Kind: domain.PlanChapter, ParentID: "a1", Title: "第二章"}, Number: 2, State: service.ChapterPending},
		{Node: domain.PlanNode{ID: "v2", Kind: domain.PlanVolume, Title: "卷二"}},
		{Node: domain.PlanNode{ID: "a2", Kind: domain.PlanArc, ParentID: "v2", Title: "弧二"}},
		{Node: domain.PlanNode{ID: "c3", Kind: domain.PlanChapter, ParentID: "a2", Title: "第三章"}, Number: 3, State: service.ChapterPlanned},
	}
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-fold", Intent: domain.Intent{Premise: "折叠", TargetChapters: 5},
		Outline: outline,
	}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0) // 第一个章行（行 2）

	// 光标移到卷一头行并折叠：整棵子树（弧一 + 两章）从可见行消失。
	m, _ = press(t, m, tea.KeyUp)
	m, _ = press(t, m, tea.KeyUp)
	if m.bench.cursor != 0 {
		t.Fatalf("cursor should reach volume header, got %d", m.bench.cursor)
	}
	m, _ = press(t, m, tea.KeyEnter)
	view := m.View()
	if !strings.Contains(view, "▸ 卷一") || !strings.Contains(view, "2 章") || !strings.Contains(view, "◐") {
		t.Fatalf("collapsed header must summarize subtree:\n%s", view)
	}
	if strings.Contains(view, "第一章") || strings.Contains(view, "弧一") {
		t.Fatalf("collapsed subtree must be hidden:\n%s", view)
	}

	// 折叠状态与光标身份跨快照刷新存活（快照每轮整体替换）。
	updated, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
	m = updated.(model)
	if !m.bench.collapsed["v1"] || m.bench.cursor != 0 {
		t.Fatalf("fold state must survive refresh: collapsed=%v cursor=%d", m.bench.collapsed, m.bench.cursor)
	}

	// 展开恢复；未规划占位行（4、5）不可选：光标到最后一章后不再下移。
	m, _ = press(t, m, tea.KeyEnter)
	if view := m.View(); !strings.Contains(view, "第一章") {
		t.Fatalf("expand must restore subtree:\n%s", view)
	}
	for i := 0; i < 10; i++ {
		m, _ = press(t, m, tea.KeyDown)
	}
	rows := m.outlineRows()
	if !rows[m.bench.cursor].isChapter() || rows[m.bench.cursor].chapter != 3 {
		t.Fatalf("cursor must stop at last chapter, got row %#v", rows[m.bench.cursor])
	}
}

func TestFoldPreferencePersistsAcrossReopen(t *testing.T) {
	// M3 布局偏好记忆：折叠随手写进用户级 state.json，重开同一作品恢复；
	// 落点（LastProjectID）与偏好互不抹除。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-pref", 1)
	m.bench.loaded = true
	m.width, m.height = 180, 40
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-pref", Intent: domain.Intent{Premise: "偏好", TargetChapters: 1},
		Outline: []service.OutlineNode{
			{Node: domain.PlanNode{ID: "v1", Kind: domain.PlanVolume, Title: "卷一"}},
			{Node: domain.PlanNode{ID: "a1", Kind: domain.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, ParentID: "a1", Title: "第一章"}, Number: 1},
		},
	}
	m.bench.cursor = 0 // 卷一头行
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.collapsed["v1"] {
		t.Fatalf("enter on header must fold: %v", m.bench.collapsed)
	}

	updated, _ := m.openProject("book-pref")
	m = updated.(model)
	if !m.bench.collapsed["v1"] {
		t.Fatalf("reopen must restore fold preference: %v", m.bench.collapsed)
	}
	state, err := app.LoadState(deps.ConfigDir)
	if err != nil || state.LastProjectID != "book-pref" || len(state.Collapsed["book-pref"]) != 1 {
		t.Fatalf("state must keep both landing and preference: %#v, %v", state, err)
	}
}

func TestMouseWheelScrollsAndClickSelectsOutline(t *testing.T) {
	// M3 鼠标热区：滚轮=指针所在栏的 ↑/↓ 语义（主区创作中翻活动历史）；
	// 点击章行选中、点击头行折叠；单栏点击标签行切视图。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-mouse", 1)
	m.bench.loaded = true
	m.width, m.height = 180, 40
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-mouse", Intent: domain.Intent{Premise: "鼠标", TargetChapters: 3},
		Outline: []service.OutlineNode{
			{Node: domain.PlanNode{ID: "v1", Kind: domain.PlanVolume, Title: "卷一"}},
			{Node: domain.PlanNode{ID: "a1", Kind: domain.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, ParentID: "a1", Title: "第一章"}, Number: 1, State: service.ChapterConfirmed},
			{Node: domain.PlanNode{ID: "c2", Kind: domain.PlanChapter, ParentID: "a1", Title: "第二章"}, Number: 2, State: service.ChapterConfirmed},
			{Node: domain.PlanNode{ID: "v2", Kind: domain.PlanVolume, Title: "卷二"}},
			{Node: domain.PlanNode{ID: "a2", Kind: domain.PlanArc, ParentID: "v2", Title: "弧二"}},
			{Node: domain.PlanNode{ID: "c3", Kind: domain.PlanChapter, ParentID: "a2", Title: "第三章"}, Number: 3, State: service.ChapterPlanned},
		},
	}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0) // c1，行 2

	click := func(x, y int) {
		updated, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		m = updated.(model)
	}
	wheel := func(x int, up bool) {
		button := tea.MouseButtonWheelDown
		if up {
			button = tea.MouseButtonWheelUp
		}
		updated, _ := m.Update(tea.MouseMsg{X: x, Y: 10, Action: tea.MouseActionPress, Button: button})
		m = updated.(model)
	}

	// 大纲行从 y=3 起（顶栏 2 行 + 栏标题 1 行）。点击第 2 章（行 3）。
	click(5, 3+3)
	if m.bench.cursor != 3 || m.bench.pane != benchPaneOutline {
		t.Fatalf("click chapter: cursor=%d pane=%d", m.bench.cursor, m.bench.pane)
	}
	// 点击卷二头行（行 4）折叠：光标落在头行，弧二与第三章消失。
	click(5, 3+4)
	if !m.bench.collapsed["v2"] || m.bench.cursor != 4 || strings.Contains(m.View(), "第三章") {
		t.Fatalf("click header must collapse: collapsed=%v cursor=%d", m.bench.collapsed, m.bench.cursor)
	}
	// 大纲上滚轮：光标上移到第 2 章（行 3）。
	wheel(5, true)
	if m.bench.cursor != 3 {
		t.Fatalf("wheel over outline: cursor=%d", m.bench.cursor)
	}
	// 创作中主区滚轮向上翻活动历史（暂停跟随）。
	m.bench.writing = true
	for i := 0; i < 3; i++ {
		hub.Publish(activity.Event{
			ProjectID: "book-mouse", RunID: "run-1", OperationID: "op-1",
			Kind: activity.ToolStart, Tool: "authority_read", CallID: fmt.Sprintf("c%d", i),
			At: time.Now().UTC(),
		})
	}
	updated, _ := m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	wheel(60, true)
	if m.bench.feedOffset != 1 {
		t.Fatalf("wheel over main must page feed history: offset=%d", m.bench.feedOffset)
	}
	wheel(60, false)
	if m.bench.feedOffset != 0 {
		t.Fatalf("wheel down must resume follow: offset=%d", m.bench.feedOffset)
	}
	// 单栏降级：点击标签行第二个标签切到正文视图。
	m.width = 90
	m.bench.writing = false
	click(8, 2)
	if m.bench.view != 1 {
		t.Fatalf("tab click must switch view, view=%d", m.bench.view)
	}
}

func TestWorkbenchTargetPromptContinuesWithNewGoal(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-1", 1)
	m.bench.loaded = true
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-1", Intent: domain.Intent{Premise: "写书", TargetChapters: 3},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = updated.(model)
	if m.bench.prompt == nil || m.bench.prompt.purpose != "target" {
		t.Fatalf("g 未进入目标输入态: %#v", m.bench.prompt)
	}
	m = typeText(t, m, "5")
	m, cmd := press(t, m, tea.KeyEnter)
	if m.bench.prompt != nil || !m.bench.writing || cmd == nil {
		t.Fatalf("target prompt did not continue run: prompt=%v writing=%v", m.bench.prompt, m.bench.writing)
	}
}

func TestWorkbenchDirectivePromptRecordsRequirement(t *testing.T) {
	// §4.9：按 i 提创作要求，作用域跟随大纲选中行；空回车只给指引不提交；
	// 文字回车入账为 Directive 并刷新工作台。
	deps, api := newTestDeps(t, true)
	ctx := context.Background()
	plan := []domain.PlanNode{
		{ID: "volume-1", Kind: domain.PlanVolume, Title: "第一卷", Summary: "入道"},
		{ID: "arc-1", Kind: domain.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "入门"},
		{ID: "chapter-plan-1", Kind: domain.PlanChapter, ParentID: "arc-1", Title: "第一章", Summary: "抵达"},
	}
	if _, err := api.CreateProject(ctx, service.CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create", UserID: "tester", Reason: "创建",
		Draft: service.ProjectDraft{Intent: domain.Intent{Premise: "写书"}, Plan: plan}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	m := newModel(ctx, deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = newWorkbenchState("book-1", 1)
	m.bench.loaded = true
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-1", Intent: domain.Intent{Premise: "写书", TargetChapters: 1},
		Outline: []service.OutlineNode{{Node: plan[0]}, {Node: plan[1]}, {Node: plan[2], Number: 1}},
	}
	m, _ = pressRune(t, m, 'i')
	if m.bench.prompt == nil || m.bench.prompt.purpose != "directive" || m.bench.prompt.scope != "plan_node:volume-1" {
		t.Fatalf("i 未进入要求输入态或作用域不对: %#v", m.bench.prompt)
	}
	m, cmd := press(t, m, tea.KeyEnter)
	if m.bench.prompt == nil || cmd != nil || m.bench.notice == "" {
		t.Fatalf("empty directive must stay in prompt with guidance: prompt=%v notice=%q", m.bench.prompt, m.bench.notice)
	}
	m = typeText(t, m, "每章结尾留钩子")
	m, cmd = press(t, m, tea.KeyEnter)
	if m.bench.prompt != nil || cmd == nil {
		t.Fatalf("directive prompt did not submit: prompt=%v", m.bench.prompt)
	}
	message := findMsg[runControlMsg](t, cmd)
	if message.err != nil || message.next != "refresh" {
		t.Fatalf("add directive message = %#v", message)
	}
	project, err := api.Project(ctx, "book-1", 0)
	if err != nil || len(project.Directives) != 1 || project.Directives[0].Scope != "plan_node:volume-1" ||
		project.Directives[0].Text != "每章结尾留钩子" {
		t.Fatalf("project directives = %#v, err = %v", project.Directives, err)
	}
}

// findMsg 执行 tea.Cmd（含 Batch）并取出第一个目标类型消息。
func TestWorkbenchActivityFeedRendersAndUnsubscribes(t *testing.T) {
	// M2 活动流：打开作品即订阅；唤醒后整读快照并以创作语言渲染；
	// 陈旧代际的唤醒被丢弃；Esc 回首页只退订（channel 关闭），绝不取消创作。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-live")
	m = updated.(model)
	if m.bench.activityCh == nil {
		t.Fatal("opening a project must subscribe to run activity")
	}
	m.width, m.height = 120, 40
	m.bench.writing = true

	publish := func(kind activity.Kind, mutate func(*activity.Event)) {
		event := activity.Event{
			ProjectID: "book-live", RunID: "run-1", OperationID: "op-1",
			Kind: kind, At: time.Now().UTC(),
		}
		if mutate != nil {
			mutate(&event)
		}
		hub.Publish(event)
	}
	// 真实时序：查阅执行起止 → 落笔的参数 delta（先于其 exec start）流入。
	publish(activity.ToolStart, func(e *activity.Event) { e.Tool = "authority_read" })
	publish(activity.ToolEnd, func(e *activity.Event) { e.Tool = "authority_read" })
	publish(activity.ToolDelta, func(e *activity.Event) { e.Tool = "workspace_put_chapter"; e.Bytes = 2048 })

	updated, cmd := m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("activity wakeup must re-arm the watcher")
	}
	view := m.View()
	for _, want := range []string{"查阅设定与前情", "落笔章节工作稿", "已接收 2.0K"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "workspace_put_chapter") {
		t.Fatalf("raw tool name leaked into view:\n%s", view)
	}

	// 陈旧代际的唤醒不得写入当前工作台。
	stale := m
	stale.bench.activity = activity.Snapshot{}
	updated, _ = stale.Update(activityMsg{gen: m.bench.gen - 1, open: true})
	if updated.(model).bench.activity.Seq != 0 {
		t.Fatal("stale wakeup consumed into workbench")
	}

	// Esc 回首页退订：排空缓冲信号后必须读到关闭（发布端无泄漏）。
	m.bench.writing = false
	wake := m.bench.activityCh
	m, _ = press(t, m, tea.KeyEsc)
	if m.page != pageHome {
		t.Fatalf("page after esc = %v", m.page)
	}
	closed := false
	for i := 0; i < 3 && !closed; i++ {
		select {
		case _, open := <-wake:
			closed = !open
		default:
			t.Fatal("channel neither closed nor signalled after unsubscribe")
		}
	}
	if !closed {
		t.Fatal("subscription channel must be closed after leaving the workbench")
	}
}

func TestWorkbenchRendersStreamingProsePreview(t *testing.T) {
	// M3 逐字预览：创作中主区在活动流下方直播提取出的正文尾部；预览是
	// 易失投影，必须带"最终以确认稿为准"的标注（三层校正链）。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-live")
	m = updated.(model)
	m.width, m.height = 120, 40
	m.bench.writing = true

	base := activity.Event{
		ProjectID: "book-live", RunID: "run-1", OperationID: "op-1",
		Tool: "workspace_put_chapter", CallID: "call-1", At: time.Now().UTC(),
	}
	delta := base
	delta.Kind, delta.Bytes = activity.ToolDelta, 512
	hub.Publish(delta)
	prose := base
	prose.Kind, prose.Text = activity.Prose, "少年在山野间奔跑，晨雾未散。"
	hub.Publish(prose)

	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	view := m.View()
	for _, want := range []string{"正文预览", "最终以确认稿为准", "少年在山野间奔跑，晨雾未散。"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestActivityShowsThinkingSummaryAndErrorReason(t *testing.T) {
	// M3：错误行随行给出原因摘要（完整诊断按 d 下钻）；Provider 提供思考文本时
	// 构思行展示尾部摘要，否则只报"构思中……"。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-think")
	m = updated.(model)
	m.width, m.height = 120, 40
	m.bench.writing = true

	base := activity.Event{ProjectID: "book-think", RunID: "run-1", OperationID: "op-1", At: time.Now().UTC()}
	fail := base
	fail.Kind, fail.Tool, fail.CallID = activity.ToolEnd, "verdict_submit", "call-1"
	fail.Err = "裁定范围与任务范围不一致"
	hub.Publish(fail)
	think := base
	think.Kind, think.Text = activity.Thinking, "需要回到第三章补一处伏笔"
	hub.Publish(think)

	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	view := m.View()
	for _, want := range []string{
		"给出审阅结论遇到问题", "裁定范围与任务范围不一致", "构思中 · 需要回到第三章补一处伏笔",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestRewriteCandidateTakesPrecedenceOverOldAuthorityChapter(t *testing.T) {
	// 重写场景旧权威正文与新候选并存：回车读到的必须是等待验收的候选稿，
	// 不能被旧稿遮住（三层校正链）。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-rw")
	m = updated.(model)
	m.width, m.height = 120, 40
	m.bench.loaded = true
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-rw", Revision: 3,
		Outline: []service.OutlineNode{{
			Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, Title: "重写稿"}, Number: 1, State: service.ChapterPending,
		}},
		Manuscript: []domain.ManuscriptChapter{{
			ID: "ch-1", Number: 1, Title: "旧稿",
			Blocks: []domain.ManuscriptBlock{{ID: "b1", Text: "旧版本正文"}},
		}},
		Candidates: []service.ChapterCandidate{{
			OperationID: "op-rw", BaseRevision: 3,
			Chapter: domain.ManuscriptChapter{
				ID: "ch-1", Number: 1, Title: "重写稿",
				Blocks: []domain.ManuscriptBlock{{ID: "b1", Text: "重写后的正文"}},
			},
		}},
	}
	m, _ = press(t, m, tea.KeyEnter)
	view := m.View()
	if !strings.Contains(view, "重写后的正文") || !strings.Contains(view, "候选稿") {
		t.Fatalf("view must show rewrite candidate:\n%s", view)
	}
	if strings.Contains(view, "旧版本正文") {
		t.Fatalf("old authority chapter must not shadow the candidate:\n%s", view)
	}
}

func TestInitialLoadOnlyToleratesNotFoundDuringWriting(t *testing.T) {
	// 建书起步唯一可宽容的瞬态是"作品尚未落库"；库损坏等真实错误在创作中
	// 也必须呈现，不得静默吞掉。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "吞错收窄测试")
	m, _ = press(t, m, tea.KeyEnter)
	updated, _ := m.Update(benchRefreshedMsg{
		gen: m.bench.gen, err: fmt.Errorf("load project: %w", store.ErrNotFound),
	})
	m = updated.(model)
	if m.bench.err != "" || m.page != pageWorkbench {
		t.Fatalf("not-found transient must be tolerated: err=%q", m.bench.err)
	}
	updated, _ = m.Update(benchRefreshedMsg{
		gen: m.bench.gen, err: errors.New("database disk image is malformed"),
	})
	m = updated.(model)
	if m.bench.err == "" || m.page != pageWorkbench {
		t.Fatalf("real error must surface during writing: err=%q page=%v", m.bench.err, m.page)
	}
}

func TestActivityFeedScrollPausesFollowAndResumes(t *testing.T) {
	// 上滚即停止跟随（§2）：主区焦点 ↑ 翻活动历史后窗口锚定，新活动到达只
	// 补偿偏移并计数提示；↓ 回到底部（0）恢复自动跟随。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-scroll")
	m = updated.(model)
	m.width, m.height = 120, 40
	m.bench.loaded, m.bench.writing = true, true
	publish := func(kind activity.Kind, callID string) {
		hub.Publish(activity.Event{
			ProjectID: "book-scroll", RunID: "run-1", OperationID: "op-1",
			Kind: kind, Tool: "workspace_list", CallID: callID, At: time.Now().UTC(),
		})
	}
	for i := 0; i < 20; i++ {
		publish(activity.ToolStart, fmt.Sprintf("c%d", i))
		publish(activity.ToolEnd, fmt.Sprintf("c%d", i))
	}
	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)

	m, _ = press(t, m, tea.KeyTab) // 焦点到主区
	m = pressTimes(t, m, tea.KeyUp, 3)
	if m.bench.feedOffset != 3 {
		t.Fatalf("feed offset after scrolling up = %d", m.bench.feedOffset)
	}
	if view := m.View(); !strings.Contains(view, "已暂停跟随") {
		t.Fatalf("paused-follow indicator missing:\n%s", view)
	}
	// 新活动到达：偏移按增量补偿，窗口锚定不动。
	publish(activity.ToolStart, "c20")
	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	if m.bench.feedOffset != 4 {
		t.Fatalf("feed offset after new entry = %d, want anchored 4", m.bench.feedOffset)
	}
	m = pressTimes(t, m, tea.KeyDown, 4)
	if m.bench.feedOffset != 0 {
		t.Fatalf("feed offset after scrolling back = %d, want following again", m.bench.feedOffset)
	}
}

func TestDecisionFailureRestoresCardFromSnapshot(t *testing.T) {
	// 裁决失败不能弄丢决定卡：错误呈现的同时重拉快照，决定卡从权威恢复，
	// 用户可直接重试而不用退出重进。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-retry")
	m = updated.(model)
	m.bench.loaded = true
	m.bench.presentDecision(nil) // decideCmd 发出时已清卡
	updated, cmd := m.Update(decisionDoneMsg{
		gen: m.bench.gen, continueAfter: true, err: errors.New("approve proposal: state conflict"),
	})
	m = updated.(model)
	if m.bench.err == "" || cmd == nil {
		t.Fatalf("failure must surface error and refresh: err=%q cmd=%v", m.bench.err, cmd)
	}
	snap := service.WorkbenchSnapshot{
		ProjectID: "book-retry",
		Decision:  &service.PendingDecision{Reason: "稿件等你确认", HasProposal: true},
	}
	updated, _ = m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: snap})
	m = updated.(model)
	if m.bench.decision == nil || !m.bench.decision.hasProposal {
		t.Fatalf("decision card must be restored from snapshot: %#v", m.bench.decision)
	}
}

func TestOutlineViewportFollowsCursorAndPaneFocusCycles(t *testing.T) {
	// 长书大纲按视口窗口化：光标在远处章节时窗口随之滚动（截断指示可见）；
	// Tab 在三栏形态下轮换栏焦点，主区焦点 ↑/↓ 翻段而不是换章。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-long")
	m = updated.(model)
	m.width, m.height = 180, 20 // 矮终端逼出大纲窗口化
	m.bench.loaded = true
	snap := service.WorkbenchSnapshot{ProjectID: "book-long", Revision: 1}
	snap.Outline = append(snap.Outline, service.OutlineNode{
		Node: domain.PlanNode{ID: "v1", Kind: domain.PlanVolume, Title: "卷一"},
	})
	for i := 1; i <= 30; i++ {
		snap.Outline = append(snap.Outline, service.OutlineNode{
			Node:   domain.PlanNode{ID: fmt.Sprintf("c%d", i), Kind: domain.PlanChapter, Title: fmt.Sprintf("第%d回", i)},
			Number: i, State: service.ChapterConfirmed,
		})
	}
	var blocks []domain.ManuscriptBlock
	for i := 1; i <= 5; i++ {
		blocks = append(blocks, domain.ManuscriptBlock{ID: fmt.Sprintf("b%d", i), Text: fmt.Sprintf("第25章第%d段", i)})
	}
	snap.Manuscript = []domain.ManuscriptChapter{{ID: "ch-25", Number: 25, Title: "第25回", Blocks: blocks}}
	m.bench.snap = snap
	m.bench.cursor = 25 // 行索引：0 是卷头行，章 N 在第 N 行
	view := m.View()
	if !strings.Contains(view, "25 第25回") || !strings.Contains(view, "前面还有") {
		t.Fatalf("outline viewport must follow cursor:\n%s", view)
	}
	m, _ = press(t, m, tea.KeyTab)
	if m.bench.pane != benchPaneMain {
		t.Fatalf("pane after tab = %d", m.bench.pane)
	}
	m, _ = press(t, m, tea.KeyDown)
	if m.bench.cursor != 25 || m.bench.previewOffset != 1 {
		t.Fatalf("main focus must page preview: cursor=%d offset=%d", m.bench.cursor, m.bench.previewOffset)
	}
	m, _ = press(t, m, tea.KeyTab)
	if m.bench.pane != benchPaneDetail {
		t.Fatalf("pane = %d, want detail", m.bench.pane)
	}
	m, _ = press(t, m, tea.KeyTab)
	if m.bench.pane != benchPaneOutline {
		t.Fatalf("pane = %d, want outline again", m.bench.pane)
	}
	m, _ = press(t, m, tea.KeyDown)
	if m.bench.cursor != 26 || m.bench.previewOffset != 0 {
		t.Fatalf("outline focus must move cursor and reset offsets: cursor=%d offset=%d", m.bench.cursor, m.bench.previewOffset)
	}
}

func TestManualSelectionPinsMainPaneDuringWriting(t *testing.T) {
	// 创作中手动选章：主区固定在选中内容上，不被运行态覆盖；
	// Esc 分层返回——先回活动流，再回欢迎页（§2/§5）。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-pin")
	m = updated.(model)
	m.width, m.height = 120, 40
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap = service.WorkbenchSnapshot{
		ProjectID: "book-pin", Revision: 2,
		Outline: []service.OutlineNode{
			{Node: domain.PlanNode{ID: "c1", Kind: domain.PlanChapter, Title: "开端"}, Number: 1, State: service.ChapterConfirmed},
			{Node: domain.PlanNode{ID: "c2", Kind: domain.PlanChapter, Title: "转折"}, Number: 2, State: service.ChapterConfirmed},
		},
		Manuscript: []domain.ManuscriptChapter{
			{ID: "ch-1", Number: 1, Title: "开端", Blocks: []domain.ManuscriptBlock{{ID: "b1", Text: "第一章内容"}}},
			{ID: "ch-2", Number: 2, Title: "转折", Blocks: []domain.ManuscriptBlock{{ID: "b1", Text: "第二章内容"}}},
		},
	}
	m, _ = press(t, m, tea.KeyDown)
	if !m.bench.pinned || m.bench.cursor != 1 {
		t.Fatalf("selection during writing must pin: pinned=%v cursor=%d", m.bench.pinned, m.bench.cursor)
	}
	view := m.View()
	if !strings.Contains(view, "创作继续进行中") || !strings.Contains(view, "第二章内容") {
		t.Fatalf("pinned main pane must show selected chapter:\n%s", view)
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.pinned || m.page != pageWorkbench {
		t.Fatalf("first esc must return to activity flow: pinned=%v page=%v", m.bench.pinned, m.page)
	}
	m, _ = press(t, m, tea.KeyEsc)
	if m.page != pageHome {
		t.Fatalf("second esc must go home, page=%v", m.page)
	}
}

func TestWritingStateAllowsPauseCancelDiagnostics(t *testing.T) {
	// 在案缺陷修复：创作进行中 p/x/d 必须可用（此前被 writing 卫语句整体屏蔽，
	// 卡死的一轮无法中途暂停/取消）；c 仍屏蔽，避免并发驱动同一轮创作。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "创作中控制测试")
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.writing {
		t.Fatal("expected writing state after creating a book")
	}
	m.bench.snap.Run = &domain.CreationRun{ID: "run-1", State: domain.RunRunning}
	m.bench.loaded = true
	if _, cmd := pressRune(t, m, 'p'); cmd == nil {
		t.Fatal("p must issue pause during writing")
	}
	if _, cmd := pressRune(t, m, 'x'); cmd == nil {
		t.Fatal("x must issue cancel during writing")
	}
	if _, cmd := pressRune(t, m, 'd'); cmd == nil {
		t.Fatal("d must load diagnostics during writing")
	}
	if _, cmd := pressRune(t, m, 'c'); cmd != nil {
		t.Fatal("c must stay blocked during writing")
	}
}

func findMsg[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		message := next()
		if batch, ok := message.(tea.BatchMsg); ok {
			for _, item := range batch {
				queue = append(queue, item)
			}
			continue
		}
		if typed, ok := message.(T); ok {
			return typed
		}
	}
	var zero T
	t.Fatalf("command did not produce %T", zero)
	return zero
}
