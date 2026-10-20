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
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/app/chief"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/workbench"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	domainmodel "github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

// testExecutor 是测试里的 LLM 执行器：接受绑定但不调模型，执行一律失败；设了 started 时
// 改为报到后一直执行到被取消，相当于一次很长的模型调用。
type testExecutor struct {
	bound   bool
	started chan struct{}
}

func (e *testExecutor) Identity() string { return "llm.agent@1" }
func (e *testExecutor) Execute(ctx context.Context, _ domainmodel.Operation) (domainmodel.OperationOutcome, error) {
	if e.started == nil {
		return domainmodel.OperationOutcome{}, errors.New("test executor does not call models")
	}
	e.started <- struct{}{}
	<-ctx.Done()
	return domainmodel.OperationOutcome{}, ctx.Err()
}
func (e *testExecutor) Bind(models.Bindings) { e.bound = true }
func (e *testExecutor) Bound() bool          { return e.bound }

// newTestDeps 装配测试应用：configured 时注入 testExecutor 并绑定一个不联网的
// 模型配置（deepseek 连接 + 假密钥），页脚与右栏按这个模型名断言。
func newTestDeps(tb testing.TB, configured bool) (Deps, *bootstrap.App) {
	if !configured {
		return testDeps(tb, nil)
	}
	return testDeps(tb, &testExecutor{})
}

// scriptedChief 让主编用脚本化的模型回话。
type scriptedChief struct{ provider *litellmtest.Provider }

func (c scriptedChief) Binding(string) (models.Binding, bool) {
	client, err := litellm.New(c.provider)
	if err != nil {
		panic(err)
	}
	return models.Binding{Provider: "test", Model: "model", Digest: "model", Chat: agentcore.Model{Client: client, Request: litellm.Request{Model: "model"}}}, true
}

// chiefDeps 是带脚本化主编的已配置测试应用。
func chiefDeps(tb testing.TB, provider *litellmtest.Provider) (Deps, *bootstrap.App) {
	return assemble(tb, &testExecutor{}, scriptedChief{provider})
}

// testDeps 用给定的执行器装配测试应用；没有执行器时是未配置的应用。
func testDeps(tb testing.TB, executor *testExecutor) (Deps, *bootstrap.App) {
	return assemble(tb, executor, nil)
}

// assemble 装配测试应用；chiefModels 为空时主编用应用自己的模型绑定。
func assemble(tb testing.TB, executor *testExecutor, chiefModels chief.Models) (Deps, *bootstrap.App) {
	tb.Helper()
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(tb.TempDir(), "ainovel.db"))
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	tb.Cleanup(func() { authorityStore.Close() })
	configDir := tb.TempDir()
	options := bootstrap.Options{ConfigDir: configDir, Interactive: true, ChiefModels: chiefModels}
	if executor != nil {
		options.Executors.LLM = executor
	}
	api := bootstrap.New(authorityStore, options)
	if executor != nil {
		if err := api.Models.Apply(testConfig(), testConfig()); err != nil {
			tb.Fatalf("bind test model: %v", err)
		}
	}
	return Deps{API: api, ConfigDir: configDir, UserID: "tester", ListModels: listedModels("deepseek-v4-flash", "deepseek-v4"), TryModel: (&trial{}).try}, api
}

func testConfig() appconfig.Config {
	return appconfig.Config{}.WithProvider("deepseek", "deepseek-v4-flash", appconfig.ProviderConfig{APIKey: "test"})
}

func typeText(t *testing.T, m model, text string) model {
	t.Helper()
	updated, _ := m.Update(textKey(text))
	return updated.(model)
}

// textKey 是一次敲进一段文字的按键：测试里整段录入，省得逐字按。
func textKey(text string) tea.KeyPressMsg {
	code, _ := utf8.DecodeRuneInString(text)
	return tea.KeyPressMsg{Code: code, Text: text}
}

var (
	shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	ctrlC    = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

func press(t *testing.T, m model, code rune) (model, tea.Cmd) {
	t.Helper()
	return pressKey(t, m, tea.KeyPressMsg{Code: code})
}

func pressKey(t *testing.T, m model, key tea.KeyPressMsg) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(key)
	return updated.(model), cmd
}

func pressRune(t *testing.T, m model, r rune) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(textKey(string(r)))
	return updated.(model), cmd
}

// submit 在常驻输入框里输入文字并回车（要求、修改意见、y 或 / 命令）。
func submit(t *testing.T, m model, text string) (model, tea.Cmd) {
	t.Helper()
	return press(t, typeText(t, m, text), tea.KeyEnter)
}

// tabTo 沿首页 Tab 顺序走到指定控件；一圈都没走到说明它不在焦点环里。
func tabTo(t *testing.T, m model, focus int) model {
	t.Helper()
	for range m.focusRing() {
		if m.home.focus == focus {
			return m
		}
		m, _ = press(t, m, tea.KeyTab)
	}
	if m.home.focus != focus {
		t.Fatalf("focus %d is not reachable by Tab", focus)
	}
	return m
}

func pressTimes(t *testing.T, m model, key rune, times int) model {
	t.Helper()
	for i := 0; i < times; i++ {
		m, _ = press(t, m, key)
	}
	return m
}

func TestHomeRequiresPremiseBeforeCreating(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m, _ = press(t, m, tea.KeyEnter)
	if m.page != pageHome || m.bench.writing || m.home.err == "" {
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
		t.Fatalf("project id = %q, want generated book id", m.bench.projectID)
	}
	// 推进在会话里进行：执行器每次都失败，重试预算耗尽后停下等用户，原因进决定卡。
	m = syncSession(t, m)
	if m.bench.writing || m.bench.decision == nil || !strings.Contains(m.bench.decision.reason, "test executor does not call models") {
		t.Fatalf("run failure not surfaced: writing=%v err=%q decision=%+v", m.bench.writing, m.bench.err, m.bench.decision)
	}
}

func TestHomeLibraryOpensExistingProject(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.Update(libraryLoadedMsg{entries: []libraryEntry{
		{id: "book-1", premise: "旧作", target: 3, written: 1, state: domainmodel.RunWaitingUser},
	}})
	m = updated.(model)
	m = tabTo(t, m, focusLibrary) // 沿首页焦点顺序进入作品库
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
	updated, _ = m.Update(sessionMsg{gen: staleGen})
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
	updated, _ = m.Update(textKey("写"))
	m = updated.(model)
	if m.home.focus != focusPremise || m.home.premise.Value() != "写" {
		t.Fatalf("focus=%d premise=%q, want premise input focused with first rune", m.home.focus, m.home.premise.Value())
	}
}

func TestLibraryDeleteRequiresDoubleConfirm(t *testing.T) {
	// 删除是不可恢复的危险操作：第一次 d 只出确认提示，按其他键取消；
	// 连按两次 d 才真正删除并刷新作品库。
	deps, api := newTestDeps(t, true)
	if _, err := api.Projects.CreateProject(context.Background(), projectdoc.CreateProjectCommand{
		ProjectID: "book-del", ChangeID: "create-del", UserID: deps.UserID,
		Reason: "删除测试", Draft: projectdoc.ProjectDraft{
			Intent: domainmodel.Intent{Premise: "写废的书"},
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
	if projects, err := api.Workbench.Library(context.Background()); err != nil || len(projects) != 0 {
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
	if err := appconfig.SaveState(deps.ConfigDir, appconfig.State{LastProjectID: "book-2"}); err != nil {
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
	waiting := domainmodel.CreationRun{ID: "run:book-1", State: domainmodel.RunWaitingUser, StateReason: "第 1 章等你确认"}
	updated, _ = m.Update(benchRefreshedMsg{
		gen: gen, snap: workbench.WorkbenchSnapshot{
			ProjectID: "book-1", Run: &waiting,
			Decision: &workbench.PendingDecision{
				Reason:   "第 1 章等你确认",
				Proposal: domainmodel.Proposal{ID: "p-1", OperationID: "op-1", Reason: "第一章候选"}, HasProposal: true,
			},
		},
	})
	m = updated.(model)
	decision := m.bench.decision
	if decision == nil || !decision.hasProposal || !decision.continueAfter {
		t.Fatalf("decision card not restored: %#v", decision)
	}
	if !strings.Contains(m.screen(), "等你决定") {
		t.Fatal("决定卡未出现在视图中")
	}
}

func TestDecisionRequiresExplicitApproveAndOtherWordsGoToTheChief(t *testing.T) {
	// 页面设计 §2：批准必须输入 y 回车；空回车不提交、打开全部内容；其他文字交给主编
	// （不满意的理由由主编转成退稿），决定卡原样留着。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-1")
	m.bench.loaded = true
	present := func() {
		m.bench.presentDecision(&decisionState{
			reason: "第 1 章写好了，等你确认", continueAfter: true,
			proposal: domainmodel.Proposal{ID: "p-1", Reason: "第一章候选"}, hasProposal: true,
		})
	}
	present()
	if !strings.Contains(ansi.Strip(m.screen()), "等你决定") || !strings.Contains(ansi.Strip(m.screen()), "y 通过") {
		t.Fatal("决定卡或输入指引未出现在视图中")
	}
	// 空回车：不提交，打开全部内容。
	m, cmd := press(t, m, tea.KeyEnter)
	if m.bench.decision == nil || cmd != nil || !m.bench.reading {
		t.Fatalf("空回车应打开全部内容且不提交：decision=%v reading=%v err=%q", m.bench.decision, m.bench.reading, m.bench.err)
	}
	m, _ = press(t, m, tea.KeyEsc)
	// 单独的 y 只是打进输入框，回车才批准。
	m, cmd = pressRune(t, m, 'y')
	if m.bench.decision == nil || cmd != nil || m.bench.input.Value() != "y" {
		t.Fatalf("y 不回车不应批准：decision=%v input=%q", m.bench.decision, m.bench.input.Value())
	}
	m, cmd = press(t, m, tea.KeyEnter)
	if m.bench.decision != nil || cmd == nil || m.bench.input.Value() != "" {
		t.Fatal("y 回车未派发批准")
	}
	// 文字回车交给主编：切到对话，决定卡不动，由主编按理由退稿。
	present()
	m, cmd = submit(t, m, "开头太平淡")
	if m.bench.decision == nil || m.bench.input.Value() != "" || cmd == nil || m.bench.view != viewTalk {
		t.Fatalf("文字应交给主编：decision=%v input=%q view=%v", m.bench.decision, m.bench.input.Value(), m.bench.view)
	}
	m.bench.presentDecision(nil)
	// 没有待裁决稿件时，y 不是批准，是提醒。
	m, cmd = submit(t, m, "y")
	if cmd != nil || !strings.Contains(m.bench.notice, "没有等你决定") {
		t.Fatalf("无稿件时 y 应提醒：notice=%q", m.bench.notice)
	}
}

func TestHomeSettingsCreateProjectWithFullIntent(t *testing.T) {
	deps, api := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "一个凡人进入修仙宗门")
	m = tabTo(t, m, focusSettings)
	m, _ = press(t, m, tea.KeyEnter)
	if m.home.mode != homeSettings {
		t.Fatalf("mode = %v, want settings", m.home.mode)
	}
	m = typeText(t, m, "都市悬疑读者")          // 受众
	m = pressTimes(t, m, tea.KeyEnter, 5) // 期待体验/必须/禁止/结局方向留空，最后一项回车完成
	// 填完回到首页并停在「开始创作」上，已填的项目写在设定行里。
	if m.home.mode != homeMain || m.home.focus != focusStart || !strings.Contains(ansi.Strip(m.screen()), "已填 受众") {
		t.Fatalf("settings did not return to start: mode=%v focus=%d", m.home.mode, m.home.focus)
	}
	m, cmd := press(t, m, tea.KeyEnter)
	if m.page != pageWorkbench || !m.bench.writing || cmd == nil {
		t.Fatalf("form create: page=%v writing=%v", m.page, m.bench.writing)
	}
	m = syncSession(t, m) // 会话推进：CreateProject 成功，QuickWrite 因执行器失败停下
	snapshot, err := api.Projects.Project(context.Background(), m.bench.projectID, domainmodel.InitialRevision)
	if err != nil || snapshot.Intent.Audience != "都市悬疑读者" || snapshot.Intent.Premise == "" {
		t.Fatalf("project intent = %#v, %v", snapshot.Intent, err)
	}
}

func TestHomeImportEntryImportsProjectionAndApprovesViaDecisionCard(t *testing.T) {
	deps, api := newTestDeps(t, true)
	ctx := context.Background()
	origin, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: "origin-book", ChangeID: "create-origin", UserID: "tester", Reason: "导出源",
		Draft: projectdoc.ProjectDraft{
			Intent: domainmodel.Intent{Premise: "旧书"},
			Plan:   []domainmodel.PlanNode{{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一", Summary: "起"}},
		},
		CreatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create origin: %v", err)
	}
	projection, err := api.Projects.ExportProject(ctx, origin.ID, origin.Revision)
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
	m = tabTo(t, m, focusImport) // 沿首页焦点顺序进入导入
	m, _ = press(t, m, tea.KeyEnter)
	if m.home.mode != homeImport {
		t.Fatalf("mode = %v, want import", m.home.mode)
	}
	m = typeText(t, m, path)
	m, cmd := press(t, m, tea.KeyEnter)
	updated, cmd := m.Update(findMsg[importDoneMsg](t, cmd))
	m = updated.(model)
	// 决定卡随快照而来：离开工作台再回来也还在。
	updated, _ = m.Update(findMsg[benchRefreshedMsg](t, cmd))
	m = updated.(model)
	decision := m.bench.decision
	if m.page != pageWorkbench || m.bench.projectID != "copied-book" ||
		decision == nil || !decision.hasProposal || decision.continueAfter {
		t.Fatalf("import landing: page=%v project=%q decision=%#v", m.page, m.bench.projectID, decision)
	}
	// 批准须输入 y 回车（页面设计 §2）。
	m, cmd = submit(t, m, "y")
	updated, _ = m.Update(findMsg[decisionDoneMsg](t, cmd))
	m = updated.(model)
	copied, err := api.Projects.Project(ctx, "copied-book", domainmodel.InitialRevision)
	if err != nil || len(copied.Plan) != 1 {
		t.Fatalf("copied book = %#v, %v", copied, err)
	}
}

func TestWorkbenchTwoPaneOutlineDetailAndCandidateReading(t *testing.T) {
	// 页面设计 §2：两栏（大纲/主区）；§3 三层校正链：待确认章节读候选稿并明确标注。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-1")
	m.bench.loaded = true
	m.width, m.height = 180, 40
	run := domainmodel.CreationRun{ID: "run:book-1", State: domainmodel.RunWaitingUser}
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-1",
		Intent:    domainmodel.Intent{Premise: "测试书", EndingDirection: "圆满"},
		Length:    novel.Length{Fixed: 3, Final: 3},
		Outline: []workbench.OutlineNode{
			{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"}},
			{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第一章"}, Number: 1, State: workbench.ChapterConfirmed},
			{Node: domainmodel.PlanNode{ID: "c2", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第二章"}, Number: 2, State: workbench.ChapterPending},
		},
		Manuscript: []domainmodel.ManuscriptChapter{{
			ID: "chapter-1", PlanNodeID: "c1", Number: 1,
			Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "正文一"}},
		}},
		Candidates: []workbench.ChapterCandidate{{
			OperationID: "op-2", BaseRevision: 3,
			Chapter: domainmodel.ManuscriptChapter{
				ID: "chapter-2", PlanNodeID: "c2", Number: 2,
				Blocks: []domainmodel.ManuscriptBlock{{ID: "b2", Text: "候选正文二"}},
			},
		}},
		Briefs: map[int]workbench.ChapterBrief{1: {
			Requirements: []workbench.BriefRequirement{{ID: "directive:d1", Text: "节奏放缓", Status: domainmodel.CheckSatisfied}},
			Facts:        []workbench.BriefFact{{Fact: workbench.Fact{Kind: domainmodel.CanonState, Subject: "沈舟", Text: "平静"}}},
		}},
		Findings: []workbench.WorkbenchFinding{{ID: "review/0", ReviewFinding: domainmodel.ReviewFinding{ChapterID: "chapter-1", Severity: domainmodel.FindingNote, Note: "伏笔呼应完整"}}},
		Run:      &run,
	}
	// 快照刷新会把光标锚定在第一个章行（此处直接注入快照，手动对齐）。
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0)
	view := ansi.Strip(m.screen())
	for _, want := range []string{"作品目录", "卷一", "✓ 01", "◇ 02"} {
		if !strings.Contains(view, want) {
			t.Fatalf("workbench view missing %q", want)
		}
	}
	m, _ = submit(t, m, "/v")
	requireContains(t, m.bench.body.View(), "要求", "✓ 节奏放缓", "（已兑现）", "审阅意见", "伏笔呼应完整", "本章设定", "状态 沈舟：平静", "创作意图")
	m, _ = press(t, m, tea.KeyEsc)
	m.bench.pane = benchPaneOutline
	// 选中第 2 章（待确认）回车 → 读候选稿并标注。
	m, _ = press(t, m, tea.KeyDown)
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.reading || !strings.Contains(m.bench.body.View(), "候选稿") {
		t.Fatalf("candidate reading: reading=%v", m.bench.reading)
	}
}

func TestOutlineFoldingCollapsesSubtreeAndAnchorsCursor(t *testing.T) {
	// M3 卷/弧折叠：回车在头行折叠/展开，折叠头行给出章数与 ◐ 摘要；
	// 折叠状态与光标身份都跨快照刷新存活；未规划占位行不可选。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-fold")
	m.bench.loaded = true
	m.width, m.height = 180, 40
	outline := []workbench.OutlineNode{
		{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"}},
		{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "弧一"}},
		{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第一章"}, Number: 1, State: workbench.ChapterConfirmed},
		{Node: domainmodel.PlanNode{ID: "c2", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第二章"}, Number: 2, State: workbench.ChapterPending},
		{Node: domainmodel.PlanNode{ID: "v2", Kind: domainmodel.PlanVolume, Title: "卷二"}},
		{Node: domainmodel.PlanNode{ID: "a2", Kind: domainmodel.PlanArc, ParentID: "v2", Title: "弧二"}},
		{Node: domainmodel.PlanNode{ID: "c3", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a2", Title: "第三章"}, Number: 3, State: workbench.ChapterPlanned},
	}
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-fold", Intent: domainmodel.Intent{Premise: "折叠"}, Length: novel.Length{Fixed: 5, Final: 5},
		Outline: outline,
	}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0) // 第一个章行（行 3，行 0 是全书）
	m.bench.pane = benchPaneOutline

	// 光标移到卷一头行并折叠：整棵子树（弧一 + 两章）从可见行消失。
	m, _ = press(t, m, tea.KeyUp)
	m, _ = press(t, m, tea.KeyUp)
	if m.bench.cursor != 1 {
		t.Fatalf("cursor should reach volume header, got %d", m.bench.cursor)
	}
	m, _ = press(t, m, tea.KeyEnter)
	view := m.screen()
	if !strings.Contains(view, "▸ 卷一") || !strings.Contains(view, "1/2") || !strings.Contains(view, "◇") {
		t.Fatalf("collapsed header must summarize subtree:\n%s", view)
	}
	if strings.Contains(view, "第一章") || strings.Contains(view, "弧一") {
		t.Fatalf("collapsed subtree must be hidden:\n%s", view)
	}

	// 折叠状态与光标身份跨快照刷新存活（快照每轮整体替换）。
	updated, _ := m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: m.bench.snap})
	m = updated.(model)
	if !m.bench.collapsed["v1"] || m.bench.cursor != 1 {
		t.Fatalf("fold state must survive refresh: collapsed=%v cursor=%d", m.bench.collapsed, m.bench.cursor)
	}

	// 展开恢复；未规划占位行（4、5）不可选：光标到最后一章后不再下移。
	m, _ = press(t, m, tea.KeyEnter)
	if view := m.screen(); !strings.Contains(view, "第一章") {
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

// 目录看起来只有卷与章两级：顶部是全书行，卷顶格、标序号，弧与章同级缩进在卷名下、用颜色
// 区分，照样能折叠。滚动规划只展开当前故事弧（D67/D73）：还没展开的弧没有折叠箭头、注明
// 待展开，回车与点击都不折叠它；下面只有空弧的卷照常可折叠；开放期末尾交代后续随创作推进
// 规划。选中行与未选中行对齐。
func TestOutlineHierarchyAndUnexpandedArcs(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-rolling")
	m.bench.loaded = true
	m.width, m.height = 180, 40
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-rolling", Intent: domainmodel.Intent{Premise: "滚动规划"},
		Length: novel.Length{Compass: &domainmodel.Compass{ScaleMax: 80, Ending: "e"}},
		Outline: []workbench.OutlineNode{
			{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "漏根"}, Volume: 1},
			{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "残页"}},
			{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第一章"}, Number: 1, State: workbench.ChapterConfirmed},
			{Node: domainmodel.PlanNode{ID: "c2", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第二章"}, Number: 2, State: workbench.ChapterInProgress},
			{Node: domainmodel.PlanNode{ID: "a2", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "清辉楼"}},
			{Node: domainmodel.PlanNode{ID: "v2", Kind: domainmodel.PlanVolume, Title: "入山"}, Volume: 2},
			{Node: domainmodel.PlanNode{ID: "a3", Kind: domainmodel.PlanArc, ParentID: "v2", Title: "云麓外门"}},
		},
	}
	rows := m.outlineRows()
	line := func(index int, selected bool) string { return ansi.Strip(m.outlineRowLine(rows[index], selected, 40)) }
	column := func(text, symbol string) int { return ansi.StringWidth(text[:strings.Index(text, symbol)]) }
	for index, want := range map[int]string{0: "全书", 1: "▾ 第 1 卷 · 漏根", 2: "▾ 残页", 3: "✓ 01", 5: "清辉楼 · 待展开", 6: "▾ 第 2 卷 · 入山", 7: "云麓外门 · 待展开", 8: "··· 后续随创作推进规划"} {
		if !strings.Contains(line(index, false), want) {
			t.Fatalf("row %d = %q, want %q", index, line(index, false), want)
		}
	}
	if len(rows) != 9 || !rows[8].placeholder {
		t.Fatalf("the open period ends with one note on later planning: %d rows", len(rows))
	}
	if strings.Contains(line(5, false), "▾") || strings.Contains(line(7, false), "▾") {
		t.Fatalf("unexpanded arcs have no fold arrow: %q %q", line(5, false), line(7, false))
	}
	if column(line(1, false), "▾") != 1 || column(line(0, false), "全书") != column(line(1, false), "第") ||
		column(line(2, false), "▾") != column(line(1, false), "第") || column(line(3, false), "✓") != column(line(2, false), "▾") ||
		column(line(5, false), "清辉楼") != column(line(2, false), "残页") {
		t.Fatalf("volumes sit flush, arcs and chapters share one level under the volume title:\n%s\n%s\n%s\n%s\n%s",
			line(0, false), line(1, false), line(2, false), line(3, false), line(5, false))
	}
	if column(line(4, true), "◉") != column(line(3, false), "✓") || column(line(2, true), "▾") != column(line(2, false), "▾") ||
		column(line(0, true), "全书") != column(line(0, false), "全书") {
		t.Fatalf("selection shifts the row: %q %q %q", line(4, true), line(2, true), line(0, true))
	}

	m.bench.pane, m.bench.cursor = benchPaneOutline, 5
	m, _ = press(t, m, tea.KeyEnter)
	if m.bench.collapsed["a2"] || len(m.outlineRows()) != len(rows) {
		t.Fatalf("enter on an unexpanded arc must not fold it: %v", m.bench.collapsed)
	}
	m.clickOutlineRow(7)
	if m.bench.collapsed["a3"] || m.bench.cursor != 7 {
		t.Fatalf("click on an unexpanded arc selects without folding: %v cursor=%d", m.bench.collapsed, m.bench.cursor)
	}
	m.clickOutlineRow(6)
	if !m.bench.collapsed["v2"] {
		t.Fatal("a volume holding unexpanded arcs still folds")
	}
	m.clickOutlineRow(2)
	folded := m.outlineRows()
	if !m.bench.collapsed["a1"] || len(folded) != 6 || !strings.HasSuffix(ansi.Strip(m.outlineRowLine(folded[2], false, 40)), "1/2") {
		t.Fatalf("an arc folds its chapters: %v", m.bench.collapsed)
	}
	// 折叠行右端是「已入稿/章数」，窄栏里名字先占位置：两字卷名在最窄目录（27 列）也完整；
	// 还没有章的不标。
	m.bench.collapsed["v1"] = true
	folded = m.outlineRows()
	first, second := ansi.Strip(m.outlineRowLine(folded[1], false, 27)), ansi.Strip(m.outlineRowLine(folded[2], false, 27))
	if !strings.HasPrefix(first, " ▸ 第 1 卷 · 漏根") || !strings.HasSuffix(first, "1/2") || ansi.StringWidth(first) != 27 ||
		strings.Contains(second, "/") {
		t.Fatalf("a folded volume keeps its name and right-aligns its progress: %q / %q", first, second)
	}
}

func TestFoldPreferencePersistsAcrossReopen(t *testing.T) {
	// M3 布局偏好记忆：折叠随手写进用户级 state.json，重开同一作品恢复；
	// 落点（LastProjectID）与偏好互不抹除。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-pref")
	m.bench.loaded = true
	m.width, m.height = 180, 40
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-pref", Intent: domainmodel.Intent{Premise: "偏好"}, Length: novel.Length{Fixed: 1, Final: 1},
		Outline: []workbench.OutlineNode{
			{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"}},
			{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第一章"}, Number: 1},
		},
	}
	m.bench.cursor, m.bench.pane = 1, benchPaneOutline // 卷一头行（行 0 是全书）
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.collapsed["v1"] {
		t.Fatalf("enter on header must fold: %v", m.bench.collapsed)
	}

	updated, _ := m.openProject("book-pref")
	m = updated.(model)
	if !m.bench.collapsed["v1"] {
		t.Fatalf("reopen must restore fold preference: %v", m.bench.collapsed)
	}
	state, err := appconfig.LoadState(deps.ConfigDir)
	if err != nil || state.LastProjectID != "book-pref" || len(state.Collapsed["book-pref"]) != 1 {
		t.Fatalf("state must keep both landing and preference: %#v, %v", state, err)
	}
}

// 目录超过一屏时滚轮只翻窗口：选中章、视图与正文都不动；点翻出来的章就地选中、窗口不跳，
// 选到窗口外或 Esc 回到跟随时窗口重新随选中行居中。
func TestWheelOverLongOutlineScrollsWithoutSelecting(t *testing.T) {
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m.page, m.width, m.height = pageWorkbench, 180, 40
	m.bench = testBench(m, "book-long")
	m.bench.loaded = true
	outline := []workbench.OutlineNode{
		{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"}},
		{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "弧一"}},
	}
	for n := 1; n <= 60; n++ {
		outline = append(outline, workbench.OutlineNode{
			Node:   domainmodel.PlanNode{ID: fmt.Sprintf("c%d", n), Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: fmt.Sprintf("第%d章", n)},
			Number: n, State: workbench.ChapterConfirmed,
		})
	}
	m.bench.snap = workbench.WorkbenchSnapshot{ProjectID: "book-long", Intent: domainmodel.Intent{Premise: "长目录"}, Length: novel.Length{Fixed: 60, Final: 60}, Outline: outline}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0)
	m.switchView(viewActivity)

	l := m.benchLayout()
	rows, cursor := len(m.outlineRows()), m.bench.cursor
	if rows <= l.outlineRows {
		t.Fatalf("outline must overflow: %d rows, %d visible", rows, l.outlineRows)
	}
	for range 10 {
		m = wheelBench(m, 5, l.outlineY+2, false)
	}
	start, _ := m.bench.outlineWindow(rows, l.outlineRows)
	if start != 10 || m.bench.cursor != cursor || m.bench.view != viewActivity || m.bench.pinned {
		t.Fatalf("wheel must only move the window: start=%d cursor=%d view=%d pinned=%v", start, m.bench.cursor, m.bench.view, m.bench.pinned)
	}
	m = clickBench(m, 5, l.outlineY)
	if now, _ := m.bench.outlineWindow(rows, l.outlineRows); m.bench.cursor != start || now != start {
		t.Fatalf("clicking a revealed chapter selects it in place: cursor=%d window=%d", m.bench.cursor, now)
	}
	m, _ = press(t, m, tea.KeyHome)
	if now, _ := m.bench.outlineWindow(rows, l.outlineRows); m.bench.cursor != 0 || now != 0 || m.bench.outlineScrolled {
		t.Fatalf("selecting outside the window recenters it: cursor=%d window=%d", m.bench.cursor, now)
	}
	m = wheelBench(m, 5, l.outlineY+2, false)
	m, _ = press(t, m, tea.KeyEsc)
	if m.bench.outlineScrolled {
		t.Fatal("Esc back to following must recenter the outline")
	}
}

func TestMouseWheelScrollsAndClickSelectsOutline(t *testing.T) {
	// 鼠标：滚轮滚指针所在栏（目录只翻窗口，主区创作中翻活动历史）；
	// 点击章行选中、点击头行折叠、点击标签切视图；坐标全部来自固定布局。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	m.gen = 1
	m.page = pageWorkbench
	m.bench = testBench(m, "book-mouse")
	m.bench.loaded = true
	m.width, m.height = 180, 40
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-mouse", Intent: domainmodel.Intent{Premise: "鼠标"}, Length: novel.Length{Fixed: 3, Final: 3},
		Outline: []workbench.OutlineNode{
			{Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"}},
			{Node: domainmodel.PlanNode{ID: "a1", Kind: domainmodel.PlanArc, ParentID: "v1", Title: "弧一"}},
			{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第一章"}, Number: 1, State: workbench.ChapterConfirmed},
			{Node: domainmodel.PlanNode{ID: "c2", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a1", Title: "第二章"}, Number: 2, State: workbench.ChapterConfirmed},
			{Node: domainmodel.PlanNode{ID: "v2", Kind: domainmodel.PlanVolume, Title: "卷二"}},
			{Node: domainmodel.PlanNode{ID: "a2", Kind: domainmodel.PlanArc, ParentID: "v2", Title: "弧二"}},
			{Node: domainmodel.PlanNode{ID: "c3", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "a2", Title: "第三章"}, Number: 3, State: workbench.ChapterPlanned},
		},
	}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0) // c1，行 3（行 0 是全书）

	click := func(x, y int) {
		updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		m = updated.(model)
	}
	wheel := func(x, y int, up bool) {
		button := tea.MouseWheelDown
		if up {
			button = tea.MouseWheelUp
		}
		updated, _ := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: button})
		m = updated.(model)
	}

	l := m.benchLayout()
	// 点击第 2 章（目录行 4）。
	click(5, l.outlineY+4)
	if m.bench.cursor != 4 || m.bench.pane != benchPaneOutline {
		t.Fatalf("click chapter: cursor=%d pane=%d", m.bench.cursor, m.bench.pane)
	}
	// 点击卷二头行（行 5）折叠：光标落在头行，弧二与第三章消失。
	click(5, l.outlineY+5)
	if !m.bench.collapsed["v2"] || m.bench.cursor != 5 || strings.Contains(m.screen(), "第三章") {
		t.Fatalf("click header must collapse: collapsed=%v cursor=%d", m.bench.collapsed, m.bench.cursor)
	}
	// 目录一屏装得下：滚轮无可翻，也不换选中章（长目录见 TestWheelOverLongOutlineScrollsWithoutSelecting）。
	wheel(5, l.outlineY, true)
	if m.bench.cursor != 5 {
		t.Fatalf("wheel over outline: cursor=%d", m.bench.cursor)
	}
	// 点击「活动」标签切视图；活动视图里滚轮向上冻结跟随，向下到底恢复。
	m.bench.writing = true
	for i := 0; i < 40; i++ {
		for _, kind := range []activity.Kind{activity.ToolStart, activity.ToolEnd} {
			hub.Publish(activity.Event{
				ProjectID: "book-mouse", RunID: "run-1", OperationID: "op-1",
				Kind: kind, Tool: "authority_read", CallID: fmt.Sprintf("c%d", i), At: time.Now().UTC(),
			})
		}
	}
	updated, _ := m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	tabs := m.benchTabs(l)
	click(tabs[2].x0, l.tabsY)
	if m.bench.view != viewActivity {
		t.Fatalf("tab click must switch view: %d", m.bench.view)
	}
	wheel(l.mainX+2, l.contentY+3, true)
	if m.bench.activityHeld == nil {
		t.Fatal("wheel up over the activity view must pause following")
	}
	for i := 0; i < 5 && m.bench.activityHeld != nil; i++ {
		wheel(l.mainX+2, l.contentY+3, false)
	}
	if m.bench.activityHeld != nil {
		t.Fatal("wheel down to the bottom must resume following")
	}
	// 正文视图里滚轮只动正文，不碰目录。
	click(tabs[1].x0, l.tabsY)
	wheel(l.mainX+5, l.contentY+3, true)
	if m.bench.cursor != 5 || m.bench.view != viewProse {
		t.Fatalf("wheel over prose leaked: cursor=%d view=%d", m.bench.cursor, m.bench.view)
	}
}

// 头部进度三态（D63）：固定篇幅给分母；AI 收官后给分母并注明；开放期是已入稿、已规划与预计篇幅。
func TestProgressLabelFollowsLength(t *testing.T) {
	for _, c := range []struct {
		length novel.Length
		want   string
	}{
		{novel.Length{Fixed: 8, Final: 8}, "已入稿 3 / 8 章"},
		{novel.Length{Compass: &domainmodel.Compass{ScaleMax: 60, Ending: "e", Final: 40}, Final: 40}, "已入稿 3 / 40 章 · 收官"},
		{novel.Length{Compass: &domainmodel.Compass{ScaleMax: 60, Ending: "e"}}, "已入稿 3 章 · 已规划 8 · 预计 60"},
		{novel.Length{}, "已入稿 3 章 · 篇幅待定"},
	} {
		if got := progressLabel(3, 8, c.length); got != c.want {
			t.Fatalf("progress %+v = %q, want %q", c.length, got, c.want)
		}
	}
}

// 决定卡把罗盘变化说成人话：AI 把预计篇幅定到护栏以上时这正是等你裁决的内容；固定篇幅下
// 罗盘只有终局方向，不出现「预计 0 章」。
func TestCompassSummaryDescribesTheChange(t *testing.T) {
	current := &domainmodel.Compass{ScaleMax: 60, Ending: "称帝"}
	threaded := &domainmodel.Compass{ScaleMax: 60, Ending: "称帝", Threads: []domainmodel.StoryThread{{ID: "t1", Name: "王位", Question: "能否称帝"}}}
	for _, c := range []struct {
		current *domainmodel.Compass
		next    domainmodel.Compass
		want    string
	}{
		{nil, domainmodel.Compass{ScaleMax: 60, Ending: "称帝"}, "预计篇幅 60 章 · 终局：称帝"},
		{nil, domainmodel.Compass{Ending: "称帝"}, "终局：称帝"},
		{current, domainmodel.Compass{ScaleMax: 120, Ending: "称帝"}, "预计篇幅 60 → 120 章"},
		{current, domainmodel.Compass{ScaleMax: 60, Ending: "称帝", Final: 48}, "收官 48 章"},
		{current, domainmodel.Compass{Ending: "归隐"}, "取消预计篇幅（原 60 章） · 终局：归隐"},
		{&domainmodel.Compass{ScaleMax: 60, Ending: "称帝", Final: 40}, domainmodel.Compass{ScaleMax: 60, Ending: "称帝"}, "撤回收官（原 40 章）"},
		{current, *current, "故事罗盘未变"},
		// 长线（D91）：新立的、改了问题的、定下或改了作答章的。
		{threaded, domainmodel.Compass{ScaleMax: 60, Ending: "称帝", Threads: []domainmodel.StoryThread{
			{ID: "t1", Name: "王位", Question: "能否守住王位", Answer: "plan-9"},
			{ID: "t2", Name: "旧债", Question: "父仇谁报"},
		}}, "长线「王位」改问：能否守住王位 · 新立长线：旧债 · 定下作答章：王位"},
		{threaded, *threaded, "故事罗盘未变"},
	} {
		if got := compassSummary(c.current, c.next); got != c.want {
			t.Fatalf("compass summary = %q; want %q", got, c.want)
		}
	}
}

func TestTypingTalksToTheChief(t *testing.T) {
	// D74：输入框里的话交给主编；主编按用户说的范围记下要求，对话视图里是用户的话、
	// 回执与回话，不再按大纲选中行猜作用域。
	ctx := context.Background()
	provider := litellmtest.New(
		litellmtest.Respond(litellm.ToolUseBlock{ID: "note", Name: "note_requirement", Arguments: `{"text":"每章结尾留钩子","scope":{"volume":1}}`}),
		litellmtest.Text("记下了，第一卷每章结尾都会留钩子。"),
	)
	deps, api := chiefDeps(t, provider)
	plan := []domainmodel.PlanNode{
		{ID: "volume-1", Kind: domainmodel.PlanVolume, Title: "第一卷", Summary: "入道"},
		{ID: "arc-1", Kind: domainmodel.PlanArc, ParentID: "volume-1", Title: "山门", Summary: "入门"},
		{ID: "chapter-plan-1", Kind: domainmodel.PlanChapter, Turn: "转折", ParentID: "arc-1", Title: "第一章", Summary: "抵达"},
	}
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create", UserID: "tester", Reason: "创建",
		Draft: projectdoc.ProjectDraft{Intent: domainmodel.Intent{Premise: "写书"}, Plan: plan}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	m := newModel(ctx, deps)
	m.gen, m.page, m.width, m.height = 1, pageWorkbench, 150, 40
	m.bench = testBench(m, "book-1")
	m.bench.loaded = true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-1", Intent: domainmodel.Intent{Premise: "写书"}, Length: novel.Length{Fixed: 1, Final: 1},
		Outline: []workbench.OutlineNode{{Node: plan[0]}, {Node: plan[1]}, {Node: plan[2], Number: 1}},
	}
	m, cmd := submit(t, m, "第一卷每章结尾留钩子")
	if m.bench.input.Value() != "" || cmd == nil || m.bench.view != viewTalk {
		t.Fatalf("words must go to the chief: input=%q view=%v", m.bench.input.Value(), m.bench.view)
	}
	if sent := cmd().(chiefSentMsg); sent.err != nil {
		t.Fatalf("send: %v", sent.err)
	}
	for m.bench.session.Busy() {
		time.Sleep(5 * time.Millisecond)
	}
	updated, _ := m.Update(sessionMsg{gen: m.bench.gen})
	m = updated.(model)
	requireContains(t, ansi.Strip(m.screen()), "› 第一卷每章结尾留钩子", "✓ 记下要求 · 第 1 卷《第一卷》 ·「每章结尾留钩子」", "记下了，第一卷每章结尾都会留钩子。")
	project, err := api.Projects.Project(ctx, "book-1", 0)
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
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-live")
	m = updated.(model)
	if m.bench.activityCh == nil {
		t.Fatal("opening a project must subscribe to run activity")
	}
	m.width, m.height = 150, 40
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
	view := m.screen()
	for _, want := range []string{"查阅设定与前情", "落笔章节工作稿", "已接收 2.0K"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "workspace_put_chapter") {
		t.Fatalf("raw tool name leaked into view:\n%s", view)
	}
	// 下一轮请求已发出、尚无增量：现场显示等待中；唤醒记下收到活动的时刻，现场的光据此走快。
	heard := m.bench.pulse.heard
	publish(activity.TurnStart, nil)
	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	if view := m.screen(); !strings.Contains(view, "等待模型回应") || !m.bench.pulse.heard.After(heard) {
		t.Fatalf("waiting indicator missing or the wakeup went unheard:\n%s", view)
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
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-live")
	m = updated.(model)
	m.width, m.height = 150, 40
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
	view := ansi.Strip(m.screen())
	for _, want := range []string{"实时预览 · 最终以入稿版本为准", "正文  少年在山野间奔跑，晨雾未散。"} {
		if !strings.Contains(view, want) {
			t.Fatalf("scene missing %q:\n%s", want, view)
		}
	}
	m, _ = press(t, m, tea.KeyF3)
	view = ansi.Strip(m.screen())
	for _, want := range []string{"正文预览 · 未入稿", "少年在山野间奔跑，晨雾未散。"} {
		if !strings.Contains(view, want) {
			t.Fatalf("activity view missing %q:\n%s", want, view)
		}
	}
}

func TestActivityShowsThinkingExcerptAndErrorReason(t *testing.T) {
	// M3：错误行随行给出原因摘要（完整诊断按 d 下钻）；Provider 提供思考文本时
	// 构思行展示尾部摘要，否则只报"构思中……"。
	deps, api := newTestDeps(t, true)
	hub := activity.NewHub()
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-think")
	m = updated.(model)
	m.width, m.height = 150, 40
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
	view := ansi.Strip(m.screen())
	for _, want := range []string{
		"给出审阅结论遇到问题", "裁定范围与任务范围不一致", "思考  需要回到第三章补一处伏笔",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

// 活动视图是完整记录：工具报错写全、保留原有分行、悬挂缩进，不像现场条那样截成一行。
func TestActivityTimelineShowsTheWholeToolError(t *testing.T) {
	cause := "实体「灰袍执事」不存在；新的人物、地点、物品或组织先在 entities 里建立：" + strings.Repeat("长", 120) + "\n第二条：关系的另一方也要先建立"
	at := time.Now()
	entry := activity.Entry{Kind: activity.ToolStart, Tool: "proposal_submit", Err: cause, Done: true, At: at, DoneAt: at.Add(5 * time.Second)}
	lines := model{}.stepLines(entry, 60)
	if head := ansi.Strip(lines[0]); !strings.Contains(head, "! 提交候选稿遇到问题，模型正在自纠") || !strings.HasSuffix(head, "5.0s") {
		t.Fatalf("head = %q", head)
	}
	var shown strings.Builder
	for _, line := range lines[1:] {
		if lipgloss.Width(line) > 60 {
			t.Fatalf("line overflows: %q", ansi.Strip(line))
		}
		shown.WriteString(strings.TrimSpace(ansi.Strip(line)) + "\n")
	}
	squash := func(text string) string { return strings.Join(strings.Fields(text), "") }
	if squash(shown.String()) != squash(cause) || !strings.Contains(shown.String(), "\n第二条") {
		t.Fatalf("error not shown whole:\n%s", shown.String())
	}
}

// 重试与失败同样在时间线上写出原始错误：首行照旧说发生了什么，原文悬挂在下面。
func TestActivityTimelineShowsRetryAndFailureCauses(t *testing.T) {
	raw := "compat: wzw.pp.ua | 521: Web server is down (HTTP 521)"
	at := time.Now()
	for head, entry := range map[string]activity.Entry{
		"↻ 连接不稳定，正在第 2 次重试":      {Kind: activity.Retry, Attempt: 2, Err: raw, Done: true, At: at},
		"! 审阅这次没能完成：模型服务繁忙或临时故障": {Kind: activity.Notice, Tone: activity.ToneFail, Text: "审阅这次没能完成：模型服务繁忙或临时故障", Err: raw, Done: true, At: at},
	} {
		lines := model{}.stepLines(entry, 100)
		if len(lines) != 2 || !strings.Contains(ansi.Strip(lines[0]), head) || strings.TrimSpace(ansi.Strip(lines[1])) != raw {
			t.Fatalf("cause not shown under %s:\n%s", entry.Kind, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
	if lines := (model{}).stepLines(activity.Entry{Kind: activity.Notice, Tone: activity.ToneDone, Text: "第 1 章已入稿", Done: true, At: at}, 100); len(lines) != 1 {
		t.Fatalf("a notice without an error stays one line: %d", len(lines))
	}
}

func TestRewriteCandidateTakesPrecedenceOverOldAuthorityChapter(t *testing.T) {
	// 重写场景旧权威正文与新候选并存：回车读到的必须是等待验收的候选稿，
	// 不能被旧稿遮住（三层校正链）。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-rw")
	m = updated.(model)
	m.width, m.height = 150, 40
	m.bench.loaded = true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-rw", Revision: 3,
		Outline: []workbench.OutlineNode{{
			Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", Title: "重写稿"}, Number: 1, State: workbench.ChapterPending,
		}},
		Manuscript: []domainmodel.ManuscriptChapter{{
			ID: "ch-1", Number: 1,
			Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "旧版本正文"}},
		}},
		Candidates: []workbench.ChapterCandidate{{
			OperationID: "op-rw", BaseRevision: 3,
			Chapter: domainmodel.ManuscriptChapter{
				ID: "ch-1", Number: 1,
				Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "重写后的正文"}},
			},
		}},
	}
	m.bench.cursor = anchorOutlineCursor(m.outlineRows(), "", 0)
	m, _ = press(t, m, tea.KeyEnter)
	view := m.screen()
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
		gen: m.bench.gen, err: fmt.Errorf("load project: %w", domainmodel.ErrNotFound),
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
	api.Workbench.AttachActivityFeed(hub)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-scroll")
	m = updated.(model)
	m.width, m.height = 150, 40
	m.bench.loaded, m.bench.writing = true, true
	publish := func(kind activity.Kind, callID string) {
		hub.Publish(activity.Event{
			ProjectID: "book-scroll", RunID: "run-1", OperationID: "op-1",
			Kind: kind, Tool: "workspace_read", CallID: callID, At: time.Now().UTC(),
		})
	}
	for i := 0; i < 40; i++ {
		publish(activity.ToolStart, fmt.Sprintf("c%d", i))
		publish(activity.ToolEnd, fmt.Sprintf("c%d", i))
	}
	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)

	m, _ = press(t, m, tea.KeyF3) // 活动视图
	m = pressTimes(t, m, tea.KeyUp, 3)
	if m.bench.activityHeld == nil {
		t.Fatal("scrolling up must hold the timeline")
	}
	if view := ansi.Strip(m.screen()); !strings.Contains(view, "已暂停跟随") {
		t.Fatalf("paused-follow indicator missing:\n%s", view)
	}
	// 新活动到达：持有的快照不变，读者所在处不动。
	before := strings.Join(m.activityView(80, 20), "\n")
	publish(activity.ToolStart, "c40")
	updated, _ = m.Update(activityMsg{gen: m.bench.gen, open: true})
	m = updated.(model)
	if after := strings.Join(m.activityView(80, 20), "\n"); after != before {
		t.Fatal("new activity moved the held timeline")
	}
	m = pressTimes(t, m, tea.KeyDown, 3)
	if m.bench.activityHeld != nil || !strings.Contains(ansi.Strip(m.screen()), "跟随最新") {
		t.Fatal("scrolling back to the bottom must resume following")
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
	if m.bench.err == "" || (cmd == nil && !m.bench.refresh.pending) {
		t.Fatalf("failure must surface error and refresh: err=%q cmd=%v", m.bench.err, cmd)
	}
	snap := workbench.WorkbenchSnapshot{
		ProjectID: "book-retry",
		Decision:  &workbench.PendingDecision{Reason: "稿件等你确认", HasProposal: true},
	}
	updated, _ = m.Update(benchRefreshedMsg{gen: m.bench.gen, snap: snap})
	m = updated.(model)
	if m.bench.decision == nil || !m.bench.decision.hasProposal {
		t.Fatalf("decision card must be restored from snapshot: %#v", m.bench.decision)
	}
}

func TestOutlineViewportFollowsCursorAndPaneFocusCycles(t *testing.T) {
	// 长书大纲按视口窗口化：光标在远处章节时窗口随之滚动（截断指示可见）；
	// Tab 在两栏之间轮换焦点，主区焦点 ↑/↓ 滚动内容而不是换章。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-long")
	m = updated.(model)
	m.width, m.height = 180, 40 // 31 行大纲装不进 19 行窗口，逼出窗口化
	m.bench.loaded = true
	snap := workbench.WorkbenchSnapshot{ProjectID: "book-long", Revision: 1}
	snap.Outline = append(snap.Outline, workbench.OutlineNode{
		Node: domainmodel.PlanNode{ID: "v1", Kind: domainmodel.PlanVolume, Title: "卷一"},
	})
	for i := 1; i <= 30; i++ {
		snap.Outline = append(snap.Outline, workbench.OutlineNode{
			Node:   domainmodel.PlanNode{ID: fmt.Sprintf("c%d", i), Kind: domainmodel.PlanChapter, Turn: "转折", Title: fmt.Sprintf("第%d回", i)},
			Number: i, State: workbench.ChapterConfirmed,
		})
	}
	var blocks []domainmodel.ManuscriptBlock
	for i := 1; i <= 30; i++ {
		blocks = append(blocks, domainmodel.ManuscriptBlock{ID: fmt.Sprintf("b%d", i), Text: fmt.Sprintf("第25章第%d段", i)})
	}
	snap.Manuscript = []domainmodel.ManuscriptChapter{{ID: "ch-25", Number: 25, Blocks: blocks}}
	m.bench.snap = snap
	m.bench.cursor = 26 // 行索引：0 是全书，1 是卷头行，章 N 在第 N+1 行
	view := m.screen()
	if !strings.Contains(view, "25  第25回") || !strings.Contains(view, "前面还有") {
		t.Fatalf("outline viewport must follow cursor:\n%s", view)
	}
	m, _ = press(t, m, tea.KeyDown)
	if m.bench.cursor != 26 || m.bench.proseOffset != 1 {
		t.Fatalf("main focus must scroll prose: cursor=%d offset=%d", m.bench.cursor, m.bench.proseOffset)
	}
	m, _ = press(t, m, tea.KeyTab)
	if m.bench.pane != benchPaneOutline {
		t.Fatalf("pane = %d, want outline", m.bench.pane)
	}
	m, _ = press(t, m, tea.KeyDown)
	if m.bench.cursor != 27 || m.bench.proseOffset != 0 {
		t.Fatalf("outline focus must move cursor and reset offsets: cursor=%d offset=%d", m.bench.cursor, m.bench.proseOffset)
	}
}

func TestManualSelectionPinsMainPaneDuringWriting(t *testing.T) {
	// 创作中手动选章：主区固定在选中内容上，不被运行态覆盖；
	// Esc 分层返回——先回活动流，再回欢迎页（§2/§5）。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	updated, _ := m.openProject("book-pin")
	m = updated.(model)
	m.width, m.height = 150, 40
	m.bench.loaded, m.bench.writing = true, true
	m.bench.snap = workbench.WorkbenchSnapshot{
		ProjectID: "book-pin", Revision: 2,
		Outline: []workbench.OutlineNode{
			{Node: domainmodel.PlanNode{ID: "c1", Kind: domainmodel.PlanChapter, Turn: "转折", Title: "开端"}, Number: 1, State: workbench.ChapterConfirmed},
			{Node: domainmodel.PlanNode{ID: "c2", Kind: domainmodel.PlanChapter, Turn: "转折", Title: "转折"}, Number: 2, State: workbench.ChapterConfirmed},
		},
		Manuscript: []domainmodel.ManuscriptChapter{
			{ID: "ch-1", Number: 1, Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "第一章内容"}}},
			{ID: "ch-2", Number: 2, Blocks: []domainmodel.ManuscriptBlock{{ID: "b1", Text: "第二章内容"}}},
		},
	}
	m.bench.pane, m.bench.cursor = benchPaneOutline, anchorOutlineCursor(m.outlineRows(), "", 0)
	m, _ = press(t, m, tea.KeyDown)
	if !m.bench.pinned || m.bench.cursor != 2 {
		t.Fatalf("selection during writing must pin: pinned=%v cursor=%d", m.bench.pinned, m.bench.cursor)
	}
	view := m.screen()
	if !strings.Contains(view, "第二章内容") {
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

func TestWritingStateKeepsDiagnosticsAndTalk(t *testing.T) {
	// 创作进行中诊断照常可用；说的话交给主编（D74），不被当成快捷键。
	deps, _ := newTestDeps(t, true)
	m := newModel(context.Background(), deps)
	m = typeText(t, m, "创作中控制测试")
	m, _ = press(t, m, tea.KeyEnter)
	if !m.bench.writing {
		t.Fatal("expected writing state after creating a book")
	}
	m.bench.snap.Run = &domainmodel.CreationRun{ID: "run-1", State: domainmodel.RunRunning}
	m.bench.loaded = true
	if _, cmd := submit(t, m, "/diag"); cmd == nil {
		t.Fatal("/diag must load diagnostics during writing")
	}
	if typed, cmd := submit(t, m, "p 开头的话也只是文字"); cmd == nil || typed.bench.input.Value() != "" || typed.bench.view != viewTalk {
		t.Fatal("plain text during writing must go to the chief")
	}
}

// testBench 是测试直接摆好的工作台：与打开作品一样带着这本书的主编会话。
func testBench(m model, projectID string) workbenchState {
	return newWorkbenchState(projectID, 1, m.api.Chief.Open(m.ctx, projectID, m.deps.UserID))
}

// syncSession 等会话的推进停下，把会话快照交给界面（相当于收到一次会话唤醒）。
func syncSession(t *testing.T, m model) model {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.bench.session.Production().Writing {
		if time.Now().After(deadline) {
			t.Fatal("the drive did not stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	updated, _ := m.Update(sessionMsg{gen: m.bench.gen})
	return updated.(model)
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
