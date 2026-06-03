# 多人格竞稿写作 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让同一章由 N 个绑定不同作者人格的 Writer 各写一稿，Judge 选优并给意见，中选 Writer 在自己的稿上润色后提交；不配置则退回原单 Writer 行为。

**Architecture:** 把"写第 N 章"从单步扩展为 `flow.Route` 状态机的多个子步骤（候选 → 选优 → 提升 → 润色提交），与现有"弧末 editor 三步走"同构，复用 checkpoint / 崩溃恢复 / 事件投影 / 工具链。竞稿由 Host 经 Route 编排，Coordinator 仍是 subagent 唯一执行者。本期串行执行，预留并发接口。

**Tech Stack:** Go 1.25，agentcore subagent 框架，charmbracelet TUI；JSONL checkpoint + 文件级 store。

**设计依据：** `docs/superpowers/specs/2026-06-02-multi-persona-contest-design.md`（已过 Codex 对抗性审查 + 独立复核）。

**关键实现细化（设计文档基础上补充）：**
- persona 候选工具 `DraftPersonaTool` 持有 persona slug，Name 仍为 `draft_chapter`（writer prompt 与 StopAfterTools 都认这个名）。其 Execute 写入目标按 store 事实决定：当本章 verdict 存在且 `promoted=true` 且 `winner==slug` 时写正式 `drafts/{ch}.draft.md`（润色阶段），否则写候选槽 `drafts/{ch}.cand-{slug}.md`（候选阶段）。这样单 config 单工具既能写候选又能润色，且 `commit_chapter` 零改动（始终读 `draft.md`）。
- 候选 writer 与中选润色 writer 是同一个 `writer_<persona>` config；StopGuard 用 `StopGuardFactory` 按"当前是否润色阶段"动态选择（候选要 `draft`、润色要 `commit`）。

---

## 文件结构

**新增：**
- `internal/domain/contest.go` — Persona、Verdict、PersonaScore 类型
- `internal/store/contest.go` — 候选槽 + verdict 存储（候选与裁定都属"竞稿工件"，与 drafts.go 同包但单独文件，职责清晰）
- `internal/tools/save_verdict.go` — Judge 的裁定落盘工具
- `internal/tools/draft_persona.go` — persona 候选/润色草稿工具
- `internal/host/persona/generator.go` — 作者名 → 文风 prompt 生成 + 缓存
- 各文件对应 `_test.go`

**修改：**
- `internal/bootstrap/config.go` — `WritingContest` 配置字段 + 归一化
- `internal/host/reminder/subagent_guards.go` — `NewCandidateStopGuard` + `NewJudgeStopGuard`
- `internal/host/flow/router.go` — 竞稿子状态机
- `internal/host/flow/state.go` — LoadState 扩展
- `internal/host/flow/dispatcher.go` — save_verdict 后内联提升
- `internal/agents/build.go` — N persona writer + judge config
- `internal/host/resume.go` — 竞稿中间态 UI 标签（可选增强）
- `config.example.jsonc`、`README.md`

**依赖顺序：** domain → config / store → tools / guards → persona → flow(state→route→dispatcher) → build → docs。

---

## Task 1: domain 竞稿类型

**Files:**
- Create: `internal/domain/contest.go`
- Test: `internal/domain/contest_test.go`

- [ ] **Step 1: 写失败测试**

```go
// internal/domain/contest_test.go
package domain

import "testing"

func TestVerdict_WinnerScore(t *testing.T) {
	v := Verdict{
		Chapter: 12,
		Winner:  "wuzei",
		Scores: []PersonaScore{
			{Persona: "wuzei", Score: 8.5, Comment: "节奏好"},
			{Persona: "tudou", Score: 7.0, Comment: "略平"},
		},
		RevisionNotes: "强化钩子",
		Promoted:      false,
	}
	if got := v.WinnerScore(); got != 8.5 {
		t.Fatalf("WinnerScore = %v, want 8.5", got)
	}
}

func TestVerdict_WinnerScore_Missing(t *testing.T) {
	v := Verdict{Winner: "ghost", Scores: []PersonaScore{{Persona: "wuzei", Score: 9}}}
	if got := v.WinnerScore(); got != 0 {
		t.Fatalf("WinnerScore for missing winner = %v, want 0", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/domain/ -run TestVerdict -v`
Expected: FAIL（`undefined: Verdict`）

- [ ] **Step 3: 写实现**

```go
// internal/domain/contest.go
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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/domain/ -run TestVerdict -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/domain/contest.go internal/domain/contest_test.go
git commit -m "feat: 新增竞稿 domain 类型 Persona/Verdict/PersonaScore"
```

---

## Task 2: config 竞稿配置字段

**Files:**
- Modify: `internal/bootstrap/config.go:106-129`（Config 结构体追加字段）
- Test: `internal/bootstrap/config_contest_test.go`

- [ ] **Step 1: 写失败测试**

```go
// internal/bootstrap/config_contest_test.go
package bootstrap

import "testing"

func TestWritingContest_Normalize_DedupTrim(t *testing.T) {
	wc := WritingContest{Personas: []string{" 乌贼 ", "土豆", "乌贼", "", "  "}}
	got := wc.Normalize()
	want := []string{"乌贼", "土豆"}
	if len(got.Personas) != len(want) {
		t.Fatalf("personas = %v, want %v", got.Personas, want)
	}
	for i := range want {
		if got.Personas[i] != want[i] {
			t.Fatalf("personas[%d] = %q, want %q", i, got.Personas[i], want[i])
		}
	}
}

func TestWritingContest_Enabled(t *testing.T) {
	if (WritingContest{}).Enabled() {
		t.Fatal("空配置应为未启用")
	}
	if !(WritingContest{Personas: []string{"乌贼", "土豆"}}).Enabled() {
		t.Fatal("两个 persona 应为启用")
	}
	// 单 persona 等价于单 writer，不算竞稿
	if (WritingContest{Personas: []string{"乌贼"}}).Normalize().Enabled() {
		t.Fatal("单 persona 不应启用竞稿")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/bootstrap/ -run TestWritingContest -v`
Expected: FAIL（`undefined: WritingContest`）

- [ ] **Step 3: 写实现**

在 `internal/bootstrap/config.go` 的 `Config` 结构体内（`ContextWindow` 字段之后、结构体闭合 `}` 之前）追加：

```go
	// 多人格竞稿配置；为空或 personas < 2 时退回单 Writer 行为（完全向后兼容）。
	WritingContest WritingContest `json:"writing_contest,omitempty"`
```

在 `Config` 结构体定义之后追加类型与方法：

```go
// WritingContest 多人格竞稿配置。
type WritingContest struct {
	// Personas 是作者名列表（如 ["乌贼","卖报小郎君","土豆"]）。
	// 数量即并行 Writer 数；< 2 时不启用竞稿。文风由启动时 LLM 依作者名生成。
	Personas []string `json:"personas,omitempty"`
	// Judge 可选，指定选优裁判模型；缺省复用 editor 角色模型。
	Judge *ModelRef `json:"judge,omitempty"`
}

// Normalize 去空白、去重、保序，返回规整后的副本。
func (w WritingContest) Normalize() WritingContest {
	seen := make(map[string]struct{}, len(w.Personas))
	out := make([]string, 0, len(w.Personas))
	for _, p := range w.Personas {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return WritingContest{Personas: out, Judge: w.Judge}
}

// Enabled 报告是否启用竞稿（至少 2 个 persona）。
func (w WritingContest) Enabled() bool { return len(w.Personas) >= 2 }
```

确认 `config.go` 已 import `"strings"`（文件顶部已有 `validateConfigText` 用到字符串处理；若缺则补 import）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/bootstrap/ -run TestWritingContest -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/bootstrap/config.go internal/bootstrap/config_contest_test.go
git commit -m "feat: config 新增 writing_contest 字段与归一化"
```

---

## Task 3: store 候选槽存储

**Files:**
- Create: `internal/store/contest.go`
- Test: `internal/store/contest_test.go`

**说明：** `Store` 已持有 `Drafts *DraftStore`（见 build.go 用法）。候选槽与 drafts 同一 `IO`，但裁定/候选属"竞稿工件"，单独建 `ContestStore` 挂到 `Store`。先看 `internal/store/store.go` 确认 `Store` 结构体如何构造各子 store（`NewDraftStore(io)` 模式），仿照添加 `Contest *ContestStore`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/contest_test.go
package store

import "testing"

// 注：store 包内已有 NewStore(dir) *Store（单返回值，无 error），
// 见 cast_test.go:11 的用法。直接用它，不另造 helper。

func TestContest_CandidateRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Contest.SaveCandidate(3, "wuzei", "乌贼的第三章"); err != nil {
		t.Fatalf("SaveCandidate: %v", err)
	}
	got, err := st.Contest.LoadCandidate(3, "wuzei")
	if err != nil {
		t.Fatalf("LoadCandidate: %v", err)
	}
	if got != "乌贼的第三章" {
		t.Fatalf("LoadCandidate = %q", got)
	}
}

func TestContest_ListCandidates(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(5, "wuzei", "a")
	_ = st.Contest.SaveCandidate(5, "tudou", "b")
	got, err := st.Contest.ListCandidates(5, []string{"wuzei", "tudou", "maibao"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("present personas = %v, want 2", got)
	}
	if !got["wuzei"] || !got["tudou"] || got["maibao"] {
		t.Fatalf("presence map wrong: %v", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestContest_Candidate -v`
Expected: FAIL（`st.Contest undefined`）

- [ ] **Step 3: 写实现（候选槽部分）**

```go
// internal/store/contest.go
package store

import (
	"fmt"
	"os"
)

// ContestStore 管理多人格竞稿的候选稿与裁定文件。
type ContestStore struct{ io *IO }

func NewContestStore(io *IO) *ContestStore { return &ContestStore{io: io} }

func candPath(chapter int, persona string) string {
	return fmt.Sprintf("drafts/%02d.cand-%s.md", chapter, persona)
}

// SaveCandidate 保存某 persona 的候选稿。
func (s *ContestStore) SaveCandidate(chapter int, persona, content string) error {
	return s.io.WriteMarkdown(candPath(chapter, persona), content)
}

// LoadCandidate 读取某 persona 的候选稿；不存在返回空串。
func (s *ContestStore) LoadCandidate(chapter int, persona string) (string, error) {
	data, err := s.io.ReadFile(candPath(chapter, persona))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ListCandidates 返回给定 persona 列表中已落盘候选稿的存在性映射。
func (s *ContestStore) ListCandidates(chapter int, personas []string) (map[string]bool, error) {
	present := make(map[string]bool, len(personas))
	for _, p := range personas {
		c, err := s.LoadCandidate(chapter, p)
		if err != nil {
			return nil, err
		}
		present[p] = c != ""
	}
	return present, nil
}
```

在 `internal/store/store.go` 的 `Store` 结构体追加字段 `Contest *ContestStore`，并在其构造处（与 `Drafts: NewDraftStore(io)` 同一位置）追加 `Contest: NewContestStore(io)`。对齐 store.go 实际的 io 变量名。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/ -run TestContest_Candidate -v` 和 `-run TestContest_ListCandidates -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/contest.go internal/store/contest_test.go internal/store/store.go
git commit -m "feat: store 新增竞稿候选槽存储 ContestStore"
```

---

## Task 4: store verdict 存储与提升

**Files:**
- Modify: `internal/store/contest.go`
- Test: `internal/store/contest_test.go`（追加）

- [ ] **Step 1: 写失败测试（追加到 contest_test.go）**

```go
func TestContest_VerdictRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	v := domain.Verdict{Chapter: 7, Winner: "wuzei", RevisionNotes: "加钩子"}
	if err := st.Contest.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}
	got, err := st.Contest.LoadVerdict(7)
	if err != nil || got == nil {
		t.Fatalf("LoadVerdict: %v / nil", err)
	}
	if got.Winner != "wuzei" || got.Promoted {
		t.Fatalf("verdict = %+v", got)
	}
}

func TestContest_PromoteCandidate(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(9, "wuzei", "中选正文")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 9, Winner: "wuzei"})

	if st.Contest.IsPromoted(9) {
		t.Fatal("提升前 IsPromoted 应为 false")
	}
	if err := st.Contest.PromoteCandidate(9, "wuzei"); err != nil {
		t.Fatalf("PromoteCandidate: %v", err)
	}
	// draft.md 应等于中选候选稿
	draft, _ := st.Drafts.LoadDraft(9)
	if draft != "中选正文" {
		t.Fatalf("draft after promote = %q", draft)
	}
	if !st.Contest.IsPromoted(9) {
		t.Fatal("提升后 IsPromoted 应为 true")
	}
	// 幂等：再提升一次不报错且结果不变
	if err := st.Contest.PromoteCandidate(9, "wuzei"); err != nil {
		t.Fatalf("PromoteCandidate 二次: %v", err)
	}
}
```

在 `contest_test.go` 顶部 import 块加入 `"github.com/voocel/ainovel-cli/internal/domain"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestContest_Verdict -v` 和 `-run TestContest_Promote -v`
Expected: FAIL（`SaveVerdict undefined`）

- [ ] **Step 3: 写实现（追加到 contest.go）**

在 `contest.go` import 块加入 `"github.com/voocel/ainovel-cli/internal/domain"`，并追加：

```go
func verdictPath(chapter int) string {
	return fmt.Sprintf("drafts/%02d.verdict.json", chapter)
}

// SaveVerdict 保存裁定结果。
func (s *ContestStore) SaveVerdict(v domain.Verdict) error {
	return s.io.WriteJSON(verdictPath(v.Chapter), v)
}

// LoadVerdict 读取裁定；不存在返回 nil。
func (s *ContestStore) LoadVerdict(chapter int) (*domain.Verdict, error) {
	var v domain.Verdict
	if err := s.io.ReadJSON(verdictPath(chapter), &v); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

// IsPromoted 报告本章中选稿是否已提升为正式 draft.md。
func (s *ContestStore) IsPromoted(chapter int) bool {
	v, err := s.LoadVerdict(chapter)
	return err == nil && v != nil && v.Promoted
}

// PromoteCandidate 把中选候选稿复制为正式 draft.md，再置 verdict.Promoted=true。
// 幂等：先复制（同内容重复无副作用）后置标记，崩溃后重做安全。
func (s *ContestStore) PromoteCandidate(chapter int, winner string) error {
	content, err := s.LoadCandidate(chapter, winner)
	if err != nil {
		return fmt.Errorf("load winner candidate: %w", err)
	}
	if content == "" {
		return fmt.Errorf("winner candidate %q for chapter %d is empty", winner, chapter)
	}
	// 复用 DraftStore 的标准路径写入，确保 commit_chapter 能读到。
	if err := s.io.WriteMarkdown(fmt.Sprintf("drafts/%02d.draft.md", chapter), content); err != nil {
		return fmt.Errorf("write promoted draft: %w", err)
	}
	v, err := s.LoadVerdict(chapter)
	if err != nil || v == nil {
		return fmt.Errorf("load verdict before mark promoted: %w", err)
	}
	v.Promoted = true
	return s.SaveVerdict(*v)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/ -run TestContest -v`
Expected: PASS（全部竞稿 store 测试）

- [ ] **Step 5: 提交**

```bash
git add internal/store/contest.go internal/store/contest_test.go
git commit -m "feat: store 竞稿 verdict 存储与幂等提升"
```

---

## Task 5: save_verdict 工具

**Files:**
- Create: `internal/tools/save_verdict.go`
- Test: `internal/tools/save_verdict_test.go`

**模板参照：** `internal/tools/save_review.go`（schema/Execute/checkpoint 写法）。checkpoint step 用 `"verdict"`，scope 用 `domain.ChapterScope(chapter)`。

- [ ] **Step 1: 写失败测试**

```go
// internal/tools/save_verdict_test.go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveVerdict_Execute(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{
		"chapter": 4,
		"winner": "wuzei",
		"scores": [{"persona":"wuzei","score":9,"comment":"好"},{"persona":"tudou","score":7,"comment":"平"}],
		"revision_notes": "强化结尾钩子"
	}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal(out, &res)
	if res["saved"] != true || res["winner"] != "wuzei" {
		t.Fatalf("result = %v", res)
	}
	v, _ := st.Contest.LoadVerdict(4)
	if v == nil || v.Winner != "wuzei" || len(v.Scores) != 2 {
		t.Fatalf("stored verdict = %+v", v)
	}
}

func TestSaveVerdict_RejectWinnerNotInScores(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{"chapter":1,"winner":"ghost","scores":[{"persona":"wuzei","score":8}],"revision_notes":"x"}`)
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("winner 不在 scores 中应报错")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/tools/ -run TestSaveVerdict -v`
Expected: FAIL（`NewSaveVerdictTool undefined`）

- [ ] **Step 3: 写实现**

```go
// internal/tools/save_verdict.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveVerdictTool 保存 Judge 对多人格候选稿的选优裁定。
type SaveVerdictTool struct {
	store *store.Store
}

func NewSaveVerdictTool(store *store.Store) *SaveVerdictTool {
	return &SaveVerdictTool{store: store}
}

func (t *SaveVerdictTool) Name() string  { return "save_verdict" }
func (t *SaveVerdictTool) Label() string { return "保存选优裁定" }
func (t *SaveVerdictTool) Description() string {
	return "保存多人格竞稿的选优裁定：从各候选稿中选出 winner（persona slug），给出各稿评分与给中选稿的修改意见。" +
		"winner 必须出现在 scores 列表中。"
}

func (t *SaveVerdictTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveVerdictTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveVerdictTool) Schema() map[string]any {
	scoreSchema := schema.Object(
		schema.Property("persona", schema.String("候选 persona slug")).Required(),
		schema.Property("score", schema.Number("评分（0-10）")).Required(),
		schema.Property("comment", schema.String("该稿的简要评语")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("winner", schema.String("中选 persona slug（必须在 scores 中）")).Required(),
		schema.Property("scores", schema.Array("各候选稿评分（每个候选一条）", scoreSchema)).Required(),
		schema.Property("revision_notes", schema.String("给中选 writer 的具体修改意见")).Required(),
	)
}

func (t *SaveVerdictTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var v domain.Verdict
	if err := json.Unmarshal(args, &v); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if v.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	if strings.TrimSpace(v.Winner) == "" {
		return nil, fmt.Errorf("winner is required")
	}
	if len(v.Scores) == 0 {
		return nil, fmt.Errorf("scores must not be empty")
	}
	inScores := false
	for _, s := range v.Scores {
		if s.Persona == v.Winner {
			inScores = true
			break
		}
	}
	if !inScores {
		return nil, fmt.Errorf("winner %q must appear in scores", v.Winner)
	}
	v.Promoted = false // 落盘时一律未提升，提升由 dispatcher 内联完成

	if err := t.store.Contest.SaveVerdict(v); err != nil {
		return nil, fmt.Errorf("save verdict: %w", err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(v.Chapter), "verdict",
		fmt.Sprintf("drafts/%02d.verdict.json", v.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint verdict: %w", err)
	}

	return json.Marshal(map[string]any{
		"saved":          true,
		"chapter":        v.Chapter,
		"winner":         v.Winner,
		"winner_score":   v.WinnerScore(),
		"revision_notes": v.RevisionNotes,
		"next_step":      "Host 将提升中选稿并指派中选 writer 润色，无需你继续操作",
	})
}
```

确认 `agentcore/schema` 有 `schema.Number`（save_review 用了 `schema.Int`）。若无 `Number`，改用 `schema.Int` 并把 `domain.PersonaScore.Score` 与测试改为整型评分（0-100），同步 domain 测试。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/tools/ -run TestSaveVerdict -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/tools/save_verdict.go internal/tools/save_verdict_test.go
git commit -m "feat: 新增 save_verdict 选优裁定工具"
```

---

## Task 6: 竞稿 StopGuard

**Files:**
- Modify: `internal/host/reminder/subagent_guards.go`
- Test: `internal/host/reminder/contest_guards_test.go`

**机制：** 复用 `newCheckpointDeltaGuard(st, agentName, requiredSteps, blockMsg)`（subagent_guards.go:32）。候选 guard 要 `draft`，judge guard 要 `verdict`。

- [ ] **Step 1: 写失败测试**

```go
// internal/host/reminder/contest_guards_test.go
package reminder

import (
	"context"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCandidateStopGuard_BlocksWithoutDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewCandidateStopGuard(st)
	dec := guard(context.Background(), agentcore.StopInfo{})
	if dec.Allow {
		t.Fatal("无 draft checkpoint 时应拦截 end_turn")
	}
}

func TestCandidateStopGuard_AllowsAfterDraft(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewCandidateStopGuard(st) // baseline 在此刻捕获
	_, _ = st.Checkpoints.AppendArtifact(domain.ChapterScope(1), "draft", "drafts/01.cand-wuzei.md")
	dec := guard(context.Background(), agentcore.StopInfo{})
	if !dec.Allow {
		t.Fatal("已有新 draft checkpoint 时应放行")
	}
}

func TestJudgeStopGuard_RequiresVerdict(t *testing.T) {
	st := store.NewStore(t.TempDir())
	guard := NewJudgeStopGuard(st)
	if guard(context.Background(), agentcore.StopInfo{}).Allow {
		t.Fatal("无 verdict 应拦截")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/host/reminder/ -run "TestCandidateStopGuard|TestJudgeStopGuard" -v`
Expected: FAIL（`NewCandidateStopGuard undefined`）

- [ ] **Step 3: 写实现（追加到 subagent_guards.go 末尾）**

```go
// NewCandidateStopGuard 要求竞稿候选 writer 本轮至少产生一次成功的 draft_chapter
// （写候选稿），而非 commit_chapter —— 候选阶段不提交终稿。
func NewCandidateStopGuard(st *store.Store) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "writer-candidate",
		[]string{"draft"},
		"你必须调用 draft_chapter 写完本章候选稿后才能结束。竞稿候选阶段不要调用 commit_chapter。",
	)
}

// NewJudgeStopGuard 要求 judge 本轮至少落盘一次 save_verdict。
func NewJudgeStopGuard(st *store.Store) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "judge",
		[]string{"verdict"},
		"你必须调用 save_verdict 保存选优裁定后才能结束。",
	)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/host/reminder/ -run "TestCandidateStopGuard|TestJudgeStopGuard" -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/host/reminder/subagent_guards.go internal/host/reminder/contest_guards_test.go
git commit -m "feat: 新增 CandidateStopGuard/JudgeStopGuard"
```

---

## Task 7: persona 候选/润色草稿工具

**Files:**
- Create: `internal/tools/draft_persona.go`
- Test: `internal/tools/draft_persona_test.go`

**核心逻辑：** 工具持有 persona slug，Name 仍为 `draft_chapter`。写入目标按 store 事实决定：本章 verdict 存在且 `promoted && winner==slug` → 写正式 `draft.md`（润色阶段）；否则写候选槽 `cand-{slug}.md`（候选阶段）。两种路径都落 `draft` checkpoint。

- [ ] **Step 1: 写失败测试**

```go
// internal/tools/draft_persona_test.go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestDraftPersona_CandidatePhase(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewDraftPersonaTool(st, "wuzei")
	if tool.Name() != "draft_chapter" {
		t.Fatalf("Name = %q, want draft_chapter", tool.Name())
	}
	args := json.RawMessage(`{"chapter":2,"content":"乌贼候选","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 候选阶段写候选槽，不写 draft.md
	cand, _ := st.Contest.LoadCandidate(2, "wuzei")
	if cand != "乌贼候选" {
		t.Fatalf("candidate = %q", cand)
	}
	draft, _ := st.Drafts.LoadDraft(2)
	if draft != "" {
		t.Fatalf("候选阶段不应写 draft.md, got %q", draft)
	}
}

func TestDraftPersona_PolishPhase(t *testing.T) {
	st := store.NewStore(t.TempDir())
	// 模拟：已裁定 wuzei 中选且已提升
	_ = st.Contest.SaveCandidate(3, "wuzei", "初稿")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 3, Winner: "wuzei", Promoted: true})

	tool := NewDraftPersonaTool(st, "wuzei")
	args := json.RawMessage(`{"chapter":3,"content":"润色后正文","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 润色阶段写正式 draft.md
	draft, _ := st.Drafts.LoadDraft(3)
	if draft != "润色后正文" {
		t.Fatalf("draft.md = %q, want 润色后正文", draft)
	}
}

func TestDraftPersona_NonWinnerStaysCandidate(t *testing.T) {
	st := store.NewStore(t.TempDir())
	// wuzei 中选并提升，但当前工具是 tudou —— tudou 仍写候选槽
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 4, Winner: "wuzei", Promoted: true})
	tool := NewDraftPersonaTool(st, "tudou")
	args := json.RawMessage(`{"chapter":4,"content":"土豆稿","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d, _ := st.Drafts.LoadDraft(4); d != "" {
		t.Fatalf("非 winner 不应写 draft.md, got %q", d)
	}
	if c, _ := st.Contest.LoadCandidate(4, "tudou"); c != "土豆稿" {
		t.Fatalf("tudou candidate = %q", c)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/tools/ -run TestDraftPersona -v`
Expected: FAIL（`NewDraftPersonaTool undefined`）

- [ ] **Step 3: 写实现**

```go
// internal/tools/draft_persona.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// DraftPersonaTool 是竞稿模式下的章节草稿工具。它绑定一个 persona slug，
// 候选阶段写隔离候选槽，润色阶段（本 persona 中选且已提升）写正式 draft.md。
// Name 仍为 draft_chapter，使 writer prompt 与 StopAfterTools 无需改动。
type DraftPersonaTool struct {
	store   *store.Store
	persona string
}

func NewDraftPersonaTool(store *store.Store, persona string) *DraftPersonaTool {
	return &DraftPersonaTool{store: store, persona: persona}
}

func (t *DraftPersonaTool) Name() string  { return "draft_chapter" }
func (t *DraftPersonaTool) Label() string { return "写入章节(竞稿)" }
func (t *DraftPersonaTool) Description() string {
	return "写入章节正文。竞稿候选阶段保存为你的候选稿；中选润色阶段保存为正式草稿。mode=write 覆盖，mode=append 追加。"
}
func (t *DraftPersonaTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *DraftPersonaTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *DraftPersonaTool) StrictSchema() bool                     { return true }

func (t *DraftPersonaTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("content", schema.String("章节正文")).Required(),
		schema.Property("mode", schema.Enum("写入模式", "write", "append")).Required(),
	)
}

func (t *DraftPersonaTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int    `json:"chapter"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", errs.ErrToolArgs)
	}

	// 判定阶段：本 persona 是否为已提升的中选稿 → 润色阶段写 draft.md。
	polish := false
	if v, _ := t.store.Contest.LoadVerdict(a.Chapter); v != nil && v.Promoted && v.Winner == t.persona {
		polish = true
	}

	var artifact string
	if polish {
		artifact = fmt.Sprintf("drafts/%02d.draft.md", a.Chapter)
		if a.Mode == "append" {
			if err := t.store.Drafts.AppendDraft(a.Chapter, a.Content); err != nil {
				return nil, fmt.Errorf("append draft: %w", err)
			}
		} else if err := t.store.Drafts.SaveDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("save draft: %w", err)
		}
	} else {
		artifact = fmt.Sprintf("drafts/%02d.cand-%s.md", a.Chapter, t.persona)
		// 候选稿支持 append：先读旧的再合并（与 DraftStore.AppendDraft 同语义）。
		content := a.Content
		if a.Mode == "append" {
			if old, _ := t.store.Contest.LoadCandidate(a.Chapter, t.persona); old != "" {
				content = old + "\n\n" + a.Content
			}
		}
		if err := t.store.Contest.SaveCandidate(a.Chapter, t.persona, content); err != nil {
			return nil, fmt.Errorf("save candidate: %w", err)
		}
	}

	if _, err := t.store.Checkpoints.AppendArtifact(domain.ChapterScope(a.Chapter), "draft", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint draft: %w", err)
	}

	phase := "candidate"
	if polish {
		phase = "polish"
	}
	return json.Marshal(map[string]any{
		"written":    true,
		"chapter":    a.Chapter,
		"persona":    t.persona,
		"phase":      phase,
		"mode":       a.Mode,
		"word_count": utf8.RuneCountInString(a.Content),
		"next_step":  "候选阶段：写完即可结束；润色阶段：继续 check_consistency 后 commit_chapter",
	})
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/tools/ -run TestDraftPersona -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/tools/draft_persona.go internal/tools/draft_persona_test.go
git commit -m "feat: 新增 persona 候选/润色草稿工具 DraftPersonaTool"
```

---

## Task 8: persona 文风生成器

**Files:**
- Create: `internal/host/persona/generator.go`
- Test: `internal/host/persona/generator_test.go`

**职责：** 输入作者名列表，输出 `[]domain.Persona`（含 slug + LLM 生成的 style_block），并缓存到 store 的 `personas.json`。注入 LLM 调用为函数依赖以便测试。slug 生成：ASCII 作者名转小写连字符；非 ASCII（中文作者名）用 `persona{序号}` 保证稳定唯一。

- [ ] **Step 1: 写失败测试**

```go
// internal/host/persona/generator_test.go
package persona

import (
	"context"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestGenerate_UsesCacheOnSecondCall(t *testing.T) {
	st := store.NewStore(t.TempDir())
	calls := 0
	gen := func(ctx context.Context, author string) (string, error) {
		calls++
		return "文风:" + author, nil
	}
	g := New(st, gen)

	authors := []string{"乌贼", "土豆"}
	p1, err := g.EnsurePersonas(context.Background(), authors)
	if err != nil {
		t.Fatalf("first EnsurePersonas: %v", err)
	}
	if len(p1) != 2 || calls != 2 {
		t.Fatalf("first call personas=%d calls=%d", len(p1), calls)
	}
	// 第二次应命中缓存，不再调 LLM
	p2, err := g.EnsurePersonas(context.Background(), authors)
	if err != nil {
		t.Fatalf("second EnsurePersonas: %v", err)
	}
	if calls != 2 {
		t.Fatalf("缓存未命中，calls=%d", calls)
	}
	if p2[0].StyleBlock != "文风:乌贼" {
		t.Fatalf("cached style = %q", p2[0].StyleBlock)
	}
}

func TestGenerate_FallbackOnError(t *testing.T) {
	st := store.NewStore(t.TempDir())
	gen := func(ctx context.Context, author string) (string, error) {
		return "", context.DeadlineExceeded
	}
	g := New(st, gen)
	got, err := g.EnsurePersonas(context.Background(), []string{"乌贼"})
	if err != nil {
		t.Fatalf("生成失败应兜底而非报错: %v", err)
	}
	if !got[0].Fallback || got[0].StyleBlock == "" {
		t.Fatalf("应使用兜底文案, got %+v", got[0])
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/host/persona/ -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 写实现**

```go
// internal/host/persona/generator.go
package persona

import (
	"context"
	"fmt"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// StyleGenFunc 依作者名生成文风 prompt 片段。注入以便测试与解耦具体 LLM。
type StyleGenFunc func(ctx context.Context, author string) (string, error)

// Generator 负责生成并缓存写作人格。
type Generator struct {
	store *store.Store
	gen   StyleGenFunc
}

func New(store *store.Store, gen StyleGenFunc) *Generator {
	return &Generator{store: store, gen: gen}
}

// EnsurePersonas 返回与 authors 对应的人格列表：命中缓存直接返回，
// 缺失的逐个生成（失败用兜底文案），最后整体写回缓存。
func (g *Generator) EnsurePersonas(ctx context.Context, authors []string) ([]domain.Persona, error) {
	cached, _ := g.store.Contest.LoadPersonas() // map[author]Persona，不存在返回空 map
	out := make([]domain.Persona, 0, len(authors))
	dirty := false

	for i, author := range authors {
		if p, ok := cached[author]; ok && p.StyleBlock != "" {
			out = append(out, p)
			continue
		}
		slug := slugFor(author, i)
		style, err := g.gen(ctx, author)
		p := domain.Persona{Slug: slug, Author: author, StyleBlock: style}
		if err != nil || style == "" {
			p.StyleBlock = fmt.Sprintf("请尽量模仿网文作者「%s」的文风进行创作：在句式节奏、用词习惯、叙事视角与情绪渲染上贴近其代表作的特征。", author)
			p.Fallback = true
		}
		out = append(out, p)
		if cached == nil {
			cached = make(map[string]domain.Persona)
		}
		cached[author] = p
		dirty = true
	}

	if dirty {
		if err := g.store.Contest.SavePersonas(cached); err != nil {
			return out, fmt.Errorf("cache personas: %w", err)
		}
	}
	return out, nil
}

// slugFor 生成稳定 slug：纯 ASCII 作者名转小写（空格转连字符），
// 含非 ASCII（中文）则回退 persona{序号}，保证唯一稳定。
func slugFor(author string, index int) string {
	ascii := true
	for _, r := range author {
		if r > unicode.MaxASCII {
			ascii = false
			break
		}
	}
	if !ascii {
		return fmt.Sprintf("persona%d", index+1)
	}
	out := make([]rune, 0, len(author))
	for _, r := range author {
		switch {
		case unicode.IsSpace(r):
			out = append(out, '-')
		default:
			out = append(out, unicode.ToLower(r))
		}
	}
	return string(out)
}
```

在 `internal/store/contest.go` 追加人格缓存读写（personas.json 存 `map[author]Persona`）：

```go
// SavePersonas 缓存人格映射（key=作者名）。
func (s *ContestStore) SavePersonas(m map[string]domain.Persona) error {
	return s.io.WriteJSON("personas.json", m)
}

// LoadPersonas 读取人格缓存；不存在返回空 map。
func (s *ContestStore) LoadPersonas() (map[string]domain.Persona, error) {
	m := make(map[string]domain.Persona)
	if err := s.io.ReadJSON("personas.json", &m); err != nil {
		if os.IsNotExist(err) {
			return map[string]domain.Persona{}, nil
		}
		return nil, err
	}
	return m, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/host/persona/ -v` 和 `go test ./internal/store/ -run TestContest -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/host/persona/ internal/store/contest.go
git commit -m "feat: 新增 persona 文风生成器与缓存"
```

---

## Task 9: flow State/LoadState 竞稿事实扩展

**Files:**
- Modify: `internal/host/flow/state.go`、`internal/host/flow/router.go:25-49`（State 结构体）
- Test: `internal/host/flow/contest_state_test.go`

**说明：** Route 是纯函数，新增竞稿事实必须经 `State` 显式传入。State 需要知道：是否启用竞稿、persona slug 列表、当前章各候选稿到位情况、verdict 是否存在与 winner、是否已提升。LoadState 从 store 读这些。

- [ ] **Step 1: 写失败测试**

```go
// internal/host/flow/contest_state_test.go
package flow

import (
	"testing"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestLoadState_ContestFacts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	// 该测试只验证 LoadState 不 panic 且竞稿字段有默认零值；
	// 具体竞稿编排由 Route 测试覆盖（Task 10）。
	s := LoadStateWithContest(st, ContestConfig{Personas: []string{"wuzei", "tudou"}})
	if !s.ContestEnabled {
		t.Fatal("配置两 persona 应 ContestEnabled=true")
	}
	if len(s.Personas) != 2 {
		t.Fatalf("personas = %v", s.Personas)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/host/flow/ -run TestLoadState_ContestFacts -v`
Expected: FAIL（`LoadStateWithContest undefined`）

- [ ] **Step 3: 写实现**

在 `router.go` 的 `State` 结构体追加竞稿字段：

```go
	// 竞稿事实（ContestEnabled=false 时其余字段无意义）。
	ContestEnabled  bool            // 是否启用多人格竞稿
	Personas        []string        // persona slug 列表，顺序即写作顺序
	ContestChapter  int             // 当前竞稿目标章（= NextChapter），0 表示不适用
	CandidatesReady map[string]bool // 各 persona 候选稿是否到位
	HasVerdict      bool            // 本章是否已有裁定
	VerdictWinner   string          // 中选 persona slug
	Promoted        bool            // 中选稿是否已提升为正式 draft.md
```

在 `state.go` 追加（保留原 `LoadState` 不变，新增带竞稿配置的入口；调用方传入 persona slug 列表）：

```go
// ContestConfig 是 LoadState 需要的竞稿静态配置（来自 bootstrap.Config 解析后的 slug 列表）。
type ContestConfig struct {
	Personas []string // persona slug，顺序即写作顺序；len<2 视为未启用
}

// LoadStateWithContest 在 LoadState 基础上补齐竞稿事实。
// 非竞稿场景（cfg.Personas<2）等价于原 LoadState。
func LoadStateWithContest(store *storepkg.Store, cfg ContestConfig) State {
	s := LoadState(store)
	if len(cfg.Personas) < 2 {
		return s
	}
	s.ContestEnabled = true
	s.Personas = cfg.Personas

	if s.Progress == nil || s.Progress.Phase != domainPhaseWriting(s) {
		return s
	}
	// 只在"正常续写"语义下编排竞稿：有重写队列/审阅/弧末后处理时不介入。
	next := s.Progress.NextChapter()
	if next <= 0 {
		return s
	}
	s.ContestChapter = next
	if ready, err := store.Contest.ListCandidates(next, cfg.Personas); err == nil {
		s.CandidatesReady = ready
	}
	if v, err := store.Contest.LoadVerdict(next); err == nil && v != nil {
		s.HasVerdict = true
		s.VerdictWinner = v.Winner
		s.Promoted = v.Promoted
	}
	return s
}
```

`state.go` 需要判断 Writing 阶段。直接 import domain 并用 `domain.PhaseWriting`，把上面 `domainPhaseWriting(s)` 替换为 `domain.PhaseWriting`，并在 `state.go` import 块加入 `"github.com/voocel/ainovel-cli/internal/domain"`。即该行写作：

```go
	if s.Progress == nil || s.Progress.Phase != domain.PhaseWriting {
		return s
	}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/host/flow/ -run TestLoadState_ContestFacts -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/host/flow/state.go internal/host/flow/router.go internal/host/flow/contest_state_test.go
git commit -m "feat: flow State/LoadState 扩展竞稿事实"
```

---

## Task 10: flow Route 竞稿子状态机

**Files:**
- Modify: `internal/host/flow/router.go:140-150`（"正常续写"分支前插入竞稿编排）
- Test: `internal/host/flow/contest_route_test.go`

**子状态机（插在原第 12 步"正常续写"之前，仅当 `ContestEnabled && ContestChapter>0`）：**
1. 有 persona 缺候选稿 → 派该 persona writer 写候选
2. 候选齐、无 verdict → 派 judge 选优
3. 有 verdict、未提升 → 由 dispatcher 内联提升（Route 不产指令，返回 nil 让 dispatcher 处理；见 Task 11）
4. 已提升、本章未 commit → 派中选 writer 润色

- [ ] **Step 1: 写失败测试**

```go
// internal/host/flow/contest_route_test.go
package flow

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func contestWritingState(next int) State {
	p := &domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: false}
	// NextChapter() 基于 CompletedChapters；让 next 成为下一章
	for i := 1; i < next; i++ {
		p.CompletedChapters = append(p.CompletedChapters, i)
	}
	return State{
		Progress:       p,
		ContestEnabled: true,
		Personas:       []string{"wuzei", "tudou"},
		ContestChapter: next,
	}
}

func TestRoute_Contest_FirstCandidate(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": false, "tudou": false}
	got := Route(s)
	if got == nil || got.Agent != "writer_wuzei" {
		t.Fatalf("expected writer_wuzei, got %+v", got)
	}
	if got.Chapter != 1 {
		t.Fatalf("chapter = %d", got.Chapter)
	}
}

func TestRoute_Contest_SecondCandidate(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": false}
	got := Route(s)
	if got == nil || got.Agent != "writer_tudou" {
		t.Fatalf("expected writer_tudou, got %+v", got)
	}
}

func TestRoute_Contest_AllReady_Judge(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	got := Route(s)
	if got == nil || got.Agent != "judge" {
		t.Fatalf("expected judge, got %+v", got)
	}
}

func TestRoute_Contest_VerdictNotPromoted_ReturnsNil(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = "wuzei"
	s.Promoted = false
	if got := Route(s); got != nil {
		t.Fatalf("未提升时 Route 应返回 nil（交 dispatcher 内联提升），got %+v", got)
	}
}

func TestRoute_Contest_Promoted_Polish(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = "wuzei"
	s.Promoted = true
	got := Route(s)
	if got == nil || got.Agent != "writer_wuzei" {
		t.Fatalf("expected writer_wuzei polish, got %+v", got)
	}
	// 润色 Task 文本必须与候选 Task 不同，规避 dispatcher dedupe
	if got.Task == "写第 1 章候选" {
		t.Fatal("润色 Task 不能与候选 Task 相同")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/host/flow/ -run TestRoute_Contest -v`
Expected: FAIL

- [ ] **Step 3: 写实现**

在 `router.go` 的 `Route` 函数中，把原"12. 正常续写"那段（`next := p.NextChapter()` 起）改为：先尝试竞稿编排，未启用再回落原逻辑。即替换原第 140-150 行为：

```go
	// 11. 多人格竞稿编排（仅在正常续写语义下；重写/审阅/弧末已在上面拦截返回）
	if s.ContestEnabled && s.ContestChapter > 0 {
		if inst := routeContest(s); inst != nil {
			return inst
		}
		// routeContest 返回 nil 有两种情况：
		//   a) verdict 已存在但未提升 —— 交 dispatcher 内联提升后重算，这里返回 nil
		//   b) 本章竞稿已全部完成 —— 落到下面正常续写（NextChapter 已推进）
		// 用 contestPending 区分：仍在竞稿中途则不要 fall through 到单 writer。
		if contestPending(s) {
			return nil
		}
	}

	// 12. 正常续写（单 Writer 或竞稿未启用）
	next := p.NextChapter()
	if next <= 0 {
		return nil
	}
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("写第 %d 章", next),
		Reason:  "续写下一章",
		Chapter: next,
	}
}

// routeContest 计算竞稿章的下一步指令；返回 nil 表示"无 writer/judge 指令需要派"
// （要么等 dispatcher 内联提升，要么本章已完成）。
func routeContest(s State) *Instruction {
	ch := s.ContestChapter
	// 1. 候选稿未齐 → 逐个派 persona writer 写候选
	for _, p := range s.Personas {
		if !s.CandidatesReady[p] {
			return &Instruction{
				Agent:   "writer_" + p,
				Task:    fmt.Sprintf("写第 %d 章候选稿", ch),
				Reason:  fmt.Sprintf("竞稿：persona %s 候选稿未完成", p),
				Chapter: ch,
			}
		}
	}
	// 2. 候选齐、无裁定 → 派 judge
	if !s.HasVerdict {
		return &Instruction{
			Agent:   "judge",
			Task:    fmt.Sprintf("评审第 %d 章的 %d 份候选稿，选优并给修改意见（save_verdict）", ch, len(s.Personas)),
			Reason:  "竞稿：候选稿已齐，待选优",
			Chapter: ch,
		}
	}
	// 3. 有裁定、未提升 → 返回 nil，由 dispatcher 内联提升
	if !s.Promoted {
		return nil
	}
	// 4. 已提升 → 派中选 writer 润色（Task 文本与候选不同，规避 dedupe）
	return &Instruction{
		Agent:   "writer_" + s.VerdictWinner,
		Task:    fmt.Sprintf("按选优意见润色并提交第 %d 章", ch),
		Reason:  fmt.Sprintf("竞稿：%s 中选，润色后提交", s.VerdictWinner),
		Chapter: ch,
	}
}

// contestPending 报告本竞稿章是否仍在中途（候选未齐 / 无裁定 / 未提升）。
// 已提升进入润色也算中途（由 routeContest 派润色指令，不会走到这里）。
func contestPending(s State) bool {
	for _, p := range s.Personas {
		if !s.CandidatesReady[p] {
			return true
		}
	}
	if !s.HasVerdict {
		return true
	}
	if !s.Promoted {
		return true
	}
	return false
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/host/flow/ -run TestRoute -v`（含原有 Route 测试，确认无回归）
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/host/flow/router.go internal/host/flow/contest_route_test.go
git commit -m "feat: flow Route 竞稿子状态机"
```

---

## Task 11: dispatcher 内联提升

**Files:**
- Modify: `internal/host/flow/dispatcher.go:61-81`（Dispatch 方法）
- Test: `internal/host/flow/dispatcher_contest_test.go`

**说明：** dispatcher 需要竞稿配置才能算 `LoadStateWithContest` 并在提升点执行提升。给 `Dispatcher` 增一个 `ContestConfig` 字段（构造时注入），`Dispatch` 改用 `LoadStateWithContest`；在 Route 前检测"有 verdict 且未提升"→ 执行 `PromoteCandidate` → 重新 LoadState。提升是纯 store 操作，提升后立即重算路由得出润色指令，同一次 Dispatch 完成。

由于 `NewDispatcher` 签名变化会触发 host.go 调用点改动，先 `grep` 调用点。

- [ ] **Step 1: 定位调用点**

Run: `grep -rn "NewDispatcher\|\.Dispatch()\|LoadState(" internal/host/ --include=*.go`
记录 host.go 中构造 Dispatcher 的位置，Task 末尾需同步更新。

- [ ] **Step 2: 写失败测试**

```go
// internal/host/flow/dispatcher_contest_test.go
package flow

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestPromoteIfNeeded_PromotesAfterVerdict(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(1, "wuzei", "中选稿")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 1, Winner: "wuzei", Promoted: false})

	cfg := ContestConfig{Personas: []string{"wuzei", "tudou"}}
	changed := PromoteIfNeeded(st, cfg, 1)
	if !changed {
		t.Fatal("应执行提升")
	}
	if !st.Contest.IsPromoted(1) {
		t.Fatal("提升标记未置位")
	}
	d, _ := st.Drafts.LoadDraft(1)
	if d != "中选稿" {
		t.Fatalf("draft = %q", d)
	}
	// 幂等：再调一次不应再"改变"
	if PromoteIfNeeded(st, cfg, 1) {
		t.Fatal("已提升后不应再次执行")
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/host/flow/ -run TestPromoteIfNeeded -v`
Expected: FAIL（`PromoteIfNeeded undefined`）

- [ ] **Step 4: 写实现**

在 `dispatcher.go` 追加导出函数（供 Dispatch 与测试共用）：

```go
// PromoteIfNeeded 在"有 verdict 且未提升"时执行中选稿提升，返回是否发生了提升。
// 纯 store 操作，幂等：已提升或无 verdict 时返回 false。
func PromoteIfNeeded(store *storepkg.Store, cfg ContestConfig, chapter int) bool {
	if len(cfg.Personas) < 2 || chapter <= 0 {
		return false
	}
	v, err := store.Contest.LoadVerdict(chapter)
	if err != nil || v == nil || v.Promoted {
		return false
	}
	if err := store.Contest.PromoteCandidate(chapter, v.Winner); err != nil {
		slog.Warn("contest promote failed", "module", "host.flow", "chapter", chapter, "winner", v.Winner, "err", err)
		return false
	}
	return true
}
```

给 `Dispatcher` 结构体增字段并在 `Dispatch` 中接入。`Dispatcher` 结构体追加：

```go
	contest ContestConfig // 竞稿配置；Personas<2 表示未启用
```

新增 setter（避免改 `NewDispatcher` 签名，降低调用点改动）：

```go
// SetContest 注入竞稿配置；Host 在启用竞稿时调用。
func (d *Dispatcher) SetContest(cfg ContestConfig) { d.contest = cfg }
```

把 `Dispatch` 方法体改为：

```go
func (d *Dispatcher) Dispatch() {
	// 竞稿：先用带竞稿事实的 State；提升点在此内联完成。
	state := LoadStateWithContest(d.store, d.contest)
	if state.ContestEnabled && state.ContestChapter > 0 && state.HasVerdict && !state.Promoted {
		if PromoteIfNeeded(d.store, d.contest, state.ContestChapter) {
			state = LoadStateWithContest(d.store, d.contest) // 重读，得到 Promoted=true
		}
	}
	inst := Route(state)
	if inst == nil {
		return
	}
	if d.dedupe(inst) {
		slog.Debug("flow router skip duplicate", "module", "host.flow", "agent", inst.Agent, "task", inst.Task)
		return
	}
	if inst.Agent == "writer" && inst.Chapter > 0 && d.store != nil {
		if err := d.store.Progress.StartChapter(inst.Chapter); err != nil {
			slog.Warn("flow router pre-mark in-progress failed", "module", "host.flow", "chapter", inst.Chapter, "err", err)
		}
	}
	msg := FormatMessage(inst)
	slog.Debug("flow router dispatch", "module", "host.flow", "agent", inst.Agent, "reason", inst.Reason)
	d.coordinator.FollowUp(agentcore.UserMsg(msg))
}
```

注意：竞稿 writer 的 agent 名是 `writer_<persona>`，上面"pre-mark in-progress"的 `inst.Agent == "writer"` 条件不命中竞稿指令——竞稿的章节进度标记由 persona writer 的 plan/draft 工具调用 `StartChapter` 自然完成（与单 writer 一致）。若需 UI 即时反映，可放宽为 `strings.HasPrefix(inst.Agent, "writer")`（含 import strings）。本期保持原条件，避免过度耦合。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/host/flow/ -v`（全包，确认无回归）
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/host/flow/dispatcher.go internal/host/flow/dispatcher_contest_test.go
git commit -m "feat: dispatcher 竞稿内联提升"
```

---

## Task 12: build.go 集成 persona writers + judge

**Files:**
- Modify: `internal/agents/build.go`
- Modify: 调用 `BuildCoordinator` 的 host.go（注入竞稿配置 + Dispatcher.SetContest）

**说明：** 这是装配层，无新单测（行为由前面各单元测试 + 手动冒烟覆盖）。改动较大，分步验证编译。

- [ ] **Step 1: 给 BuildCoordinator 增加竞稿装配**

在 `build.go` 的 `BuildCoordinator` 内，`editor` config 定义之后、`subagentTool := subagent.New(...)` 之前，插入 persona writers 与 judge 的构造。先在函数签名读取 `cfg.WritingContest`（已在 cfg 中）。新增代码：

```go
	// ---- 多人格竞稿装配 ----
	contestCfg := cfg.WritingContest.Normalize()
	var contestSubagents []subagent.Config
	var personaSlugs []string
	if contestCfg.Enabled() {
		// 生成/读取人格文风（带缓存）。生成函数走 writer 模型一次性调用。
		styleGen := func(ctx context.Context, author string) (string, error) {
			return generatePersonaStyle(ctx, writerModel, author)
		}
		personas, perr := persona.New(store, styleGen).EnsurePersonas(context.Background(), contestCfg.Personas)
		if perr != nil {
			slog.Warn("persona 生成异常，按已得结果继续", "module", "agent", "err", perr)
		}
		for _, p := range personas {
			personaSlugs = append(personaSlugs, p.Slug)
			pSlug := p.Slug
			// 每个 persona 一套工具：用 DraftPersonaTool 替换普通 draft_chapter。
			personaTools := []agentcore.Tool{
				contextTool,
				readChapter,
				tools.NewPlanChapterTool(store),
				tools.NewDraftPersonaTool(store, pSlug),
				tools.NewCheckConsistencyTool(store),
				tools.NewCommitChapterTool(store).WithRules(rulesOpts),
			}
			personaPrompt := writerPrompt + "\n\n## 你的写作人格\n" + p.StyleBlock
			contestSubagents = append(contestSubagents, subagent.Config{
				Name:               "writer_" + pSlug,
				Description:        fmt.Sprintf("竞稿写手（人格：%s）", p.Author),
				Model:              writerModel,
				SystemPrompt:       personaPrompt,
				Tools:              personaTools,
				MaxTurns:           30,
				MaxRetries:         subagentMaxRetries,
				ToolsAreIdempotent: true,
				OnMessage:          onMsg,
				// 不设 StopAfterTools：候选阶段在 draft 后由 guard 控制，
				// 润色阶段需要走到 commit。两阶段共用一个 config。
				StopGuardFactory: func(_, task string) agentcore.StopGuard {
					// Task 含"润色"→ 要 commit；否则候选阶段 → 要 draft。
					if strings.Contains(task, "润色") {
						return reminder.NewWriterStopGuard(store)
					}
					return reminder.NewCandidateStopGuard(store)
				},
			})
		}

		// Judge：本期固定复用 editor 模型。
		// 注：cfg.WritingContest.Judge 字段已保留，但 ModelSet 当前只暴露
		// ForRole/ForRoleWithFailover（models.go:110-119），无"按 ModelRef 直接解析"的入口。
		// 支持自定义 judge 模型需先给 ModelSet 加一个 judge role 或 ForRef 入口——
		// 属独立增强，本期 YAGNI 不做，judge 跟随 editor 模型。
		judgeModel := editorModel
		_ = contestCfg.Judge // 预留：未来接线自定义 judge 模型
		contestSubagents = append(contestSubagents, subagent.Config{
			Name:               "judge",
			Description:        "选优裁判：对比多份候选稿，选优并给修改意见",
			Model:              judgeModel,
			SystemPrompt:       bundle.Prompts.Judge, // 见 Step 2：新增 prompt
			Tools:              []agentcore.Tool{contextTool, readChapter, tools.NewSaveVerdictTool(store)},
			MaxTurns:           15,
			MaxRetries:         subagentMaxRetries,
			ToolsAreIdempotent: true,
			OnMessage:          onMsg,
			StopAfterTools:     []string{"save_verdict"},
			StopGuardFactory: func(_, _ string) agentcore.StopGuard {
				return reminder.NewJudgeStopGuard(store)
			},
		})
	}
```

把 `subagentTool := subagent.New(architectShort, architectLong, writer, editor)` 改为追加竞稿 subagents：

```go
	allSubagents := []subagent.Config{architectShort, architectLong, writer, editor}
	allSubagents = append(allSubagents, contestSubagents...)
	subagentTool := subagent.New(allSubagents...)
```

在 `build.go` import 块加入 `"context"` 与 `"github.com/voocel/ainovel-cli/internal/host/persona"`。

- [ ] **Step 2: 新增 judge 系统提示词与 persona 文风生成函数**

判断 `bundle.Prompts.Judge` 是否存在：

Run: `grep -rn "Judge\|Editor\b" assets/ internal/ --include=*.go | grep -i prompt | head`

若 `Prompts` 结构无 `Judge` 字段，按 `Editor` 的加载方式补一个（assets 包的 Prompts 结构体 + 对应 markdown 资源 `assets/prompts/judge.md`）。judge.md 内容要点（写入文件）：

```markdown
你是选优裁判 Judge。你会收到同一章的多份候选稿（不同作者人格所写）。

职责：
1. 用 read_chapter 逐份读取候选稿（来源为各 persona 候选槽）。
2. 从"契合大纲/人物一致/节奏钩子/文笔质感"维度对比，给每份候选 0-10 分并写评语。
3. 选出综合最佳的一份作为 winner（填其 persona slug）。
4. 给 winner 写具体、可执行的修改意见（revision_notes），供其润色。
5. 调用 save_verdict 落盘裁定。winner 必须出现在 scores 中。

只做裁定，不要自己改写正文。
```

在 `build.go` 末尾新增 persona 文风生成函数（用 writerModel 跑一次性补全）：

```go
// generatePersonaStyle 让 LLM 依作者名生成一段文风 prompt 片段。
// 失败由调用方（persona.Generator）兜底，这里只负责一次模型调用。
// 调用模式对齐项目现有写法 internal/host/imp/foundation.go:41。
func generatePersonaStyle(ctx context.Context, model agentcore.ChatModel, author string) (string, error) {
	prompt := fmt.Sprintf(
		"请用 150 字以内，描述网文作者「%s」的写作风格特征，用于指导另一个 AI 模仿其文风。"+
			"覆盖：句式节奏、用词偏好、叙事视角、情绪渲染、擅长题材。直接输出风格描述，不要前缀。",
		author,
	)
	resp, err := model.Generate(ctx, []agentcore.Message{agentcore.UserMsg(prompt)}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("model returned nil response")
	}
	return strings.TrimSpace(resp.Message.TextContent()), nil
}
```

接口已核实：`agentcore.ChatModel.Generate(ctx, []Message, []ToolSpec, ...CallOption) (*LLMResponse, error)`（model.go:269）；取文本用 `resp.Message.TextContent()`（同 foundation.go:52）；tools 传 `nil`。

- [ ] **Step 3: 在 host.go 注入竞稿配置到 Dispatcher**

在 host.go 构造 Dispatcher 后（Task 11 Step 1 已定位），加入：

```go
	if cfg.WritingContest.Normalize().Enabled() {
		slugs := /* 与 build 一致的 persona slug 列表 */
		dispatcher.SetContest(flow.ContestConfig{Personas: slugs})
	}
```

为保证 slug 列表与 build.go 完全一致，把"作者名→slug"做成 `persona` 包的导出纯函数 `persona.Slugs(authors []string) []string`（内部用 Task 8 的 slugFor 逻辑），build.go 与 host.go 都调用它。新增到 `generator.go`：

```go
// Slugs 把作者名列表转为稳定 slug 列表（与 EnsurePersonas 一致）。
func Slugs(authors []string) []string {
	out := make([]string, len(authors))
	for i, a := range authors {
		out[i] = slugFor(a, i)
	}
	return out
}
```

host.go 处改为：

```go
	wc := cfg.WritingContest.Normalize()
	if wc.Enabled() {
		dispatcher.SetContest(flow.ContestConfig{Personas: persona.Slugs(wc.Personas)})
	}
```

并确保 EnsurePersonas 内部生成的 slug 也走 `slugFor`（已是）——两边一致。

- [ ] **Step 4: 编译并跑全量测试**

Run: `go build ./... && go test ./...`
Expected: 编译通过，所有测试 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/agents/build.go internal/host/ assets/
git commit -m "feat: build/host 集成 persona writers 与 judge"
```

---

## Task 13: 文档、示例与可选 UI 标签

**Files:**
- Modify: `internal/host/resume.go:63-87`（describeResume 增竞稿标签，可选）
- Modify: `config.example.jsonc`
- Modify: `README.md`

- [ ] **Step 1: config.example.jsonc 增竞稿示例**

在 `config.example.jsonc` 适当位置（roles 之后）加入带注释的示例：

```jsonc
  // 多人格竞稿（可选）。配置 ≥2 个作者名即开启：每章由各人格各写一稿，
  // Judge 选优后中选者润色提交。不配置或 <2 个则为普通单 Writer 模式。
  // 文风由启动时 LLM 依作者名自动生成并缓存，全书一致。
  "writing_contest": {
    "personas": ["乌贼", "卖报小郎君", "土豆"],
    // judge 可选，缺省复用 editor 模型
    "judge": { "provider": "openrouter", "model": "anthropic/claude-sonnet-4.6" }
  },
```

- [ ] **Step 2: README 增竞稿说明**

在 README "特性" 列表合适位置加一条，并在配置章节补 `writing_contest` 字段说明（参照 Step 1 注释，说明：作者名→LLM 生成文风、串行执行、成本为 N 倍写作开销、不配置零成本）。

- [ ] **Step 3:（可选）resume UI 标签**

在 `resume.go` 的 `describeResume`，`PhaseWriting` 分支内、`InProgressChapter` 判断之前插入竞稿态标签（仅 UI，不影响恢复正确性）。需要 describeResume 能拿到竞稿配置——若改动面过大则跳过本步（fallthrough 到"第 N 章进行中"功能不受损，设计已说明）。简单做法：保持跳过，留 TODO 注释指向本计划。

- [ ] **Step 4: 全量验证**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add README.md config.example.jsonc internal/host/resume.go
git commit -m "docs: 竞稿配置示例与说明"
```

---

## 端到端冒烟（实现完成后）

竞稿是 LLM 驱动的长任务，单测无法覆盖真实编排。完成全部 Task 后，用一个配 2 个 persona 的最小配置实跑 3-5 章，人工确认：

- [ ] `drafts/NN.cand-*.md` 每章生成 N 份候选
- [ ] `drafts/NN.verdict.json` 含 winner + scores + `promoted:true`
- [ ] `chapters/NN.md` 终稿内容 = 中选稿润色后版本
- [ ] 中途 Ctrl-C 后 Resume，能从竞稿中断点继续而非重头
- [ ] 删除 `writing_contest` 配置，行为退回单 Writer（回归确认）

---

## Self-Review 记录

- **Spec 覆盖**：配置(T2)/人格生成缓存(T8)/persona writer 绑定工具(T7,T12)/judge+save_verdict(T5,T12)/候选槽存储(T3)/verdict+提升(T4)/Route 子状态机(T9,T10)/dispatcher 内联提升(T11)/双 guard(T6)/向后兼容(T2,T10,T12)/恢复(T10 纯函数驱动 + 冒烟验证)/文档(T13) — 全部 spec 章节有对应 Task。
- **类型一致性**：`domain.Verdict`/`PersonaScore`/`Persona` 字段在 T1 定义，T3-T12 引用一致；`ContestConfig.Personas`（slug）贯穿 T9-T12；`writer_<slug>` 命名贯穿 T10/T12；`NewCandidateStopGuard`/`NewJudgeStopGuard`/`NewDraftPersonaTool`/`NewSaveVerdictTool`/`PromoteIfNeeded`/`LoadStateWithContest`/`persona.Slugs` 跨 Task 引用一致。
- **外部接口核实结果**（写计划时已对照真实源码确认）：
  - ✅ store 构造入口是 `NewStore(dir) *Store`（单返回值，store.go:33）—— 计划已用此名。
  - ✅ `schema.Number(desc)` 存在（agentcore/schema/schema.go:54）—— save_verdict 评分用 Number。
  - ✅ `agentcore.ChatModel.Generate(ctx, []Message, []ToolSpec, ...CallOption) (*LLMResponse, error)`（model.go:269）；取文本 `resp.Message.TextContent()` —— generatePersonaStyle 已对齐。
  - ✅ `models.ForRoleWithFailover(role, report)`（models.go:119）—— writer/editor 模型走它；judge 本期复用 editorModel（`ForRef` 类入口不存在，已在 T12 注明 YAGNI 不做自定义 judge 模型）。
  - ⚠️ **仍需实现期 grep 确认**（2 处，计划内已给指引与 fallback）：① `assets.Bundle.Prompts` 是否有 `Judge` 字段（T12 Step2：无则按 Editor 模式补 + 新增 assets/prompts/judge.md）；② host.go 中 `NewDispatcher` 构造点位置（T11 Step1 grep 定位，用 `SetContest` setter 注入，不改构造签名）。
