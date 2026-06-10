# 全自动引擎加固实施计划（A1 伏笔兜底 / A2 角色硬约束 / B4 预算控制 / 竞稿两段式）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 ainovel-cli 补齐全自动长跑模式的四块短板：伏笔回收期限与未知 ID 的机械兜底、死亡角色出场的机械检查、全书成本预算门禁、竞稿两段式（先竞梗概再写全文）降本。

**Architecture:** 全部遵循项目铁律——工具只返事实不阻断（violations 进返回 JSON，由 editor/diag 升级）；Router 保持纯函数（新事实经 State 显式传入）；store 写入走原子 IO。四个 Phase 相互独立，每个 Phase 单独可交付、可单独合并。

**Tech Stack:** Go 1.25.5（无新依赖）。测试用标准 `testing` 包，store 测试用 `store.NewStore(t.TempDir())` 固件（参照 `internal/store/world_test.go` 现有写法）。

**执行环境注意：** Windows 11 + PowerShell。所有命令用 `go test ./internal/... -run <Name> -v` 形式。提交信息用中文、风格对齐 git log（如 `feat: 候选并发执行`）。**每个 Task 内的代码若与现仓库实际行号/上下文有漂移，以当前文件实际内容为准做等价插入。**

---

## Phase 1 — A1 伏笔机械兜底（deadline 期限 + 未知 ID 事实化）

现状：`ForeshadowEntry` 无期限概念；`WorldStore.UpdateForeshadow` 对 advance/resolve 未知 ID **静默忽略**（`internal/store/world.go:120-128`）；诊断只有 `StaleForeshadow`（停滞检测），无逾期检测。

### Task 1: domain — ForeshadowEntry.Deadline 字段 + OverdueForeshadow 帮助函数

**Files:**
- Modify: `internal/domain/review.go:11-25`
- Test: `internal/domain/review_test.go`（若不存在则新建）

- [ ] **Step 1: 写失败测试**

在 `internal/domain/review_test.go` 追加（文件不存在则新建，package domain）：

```go
package domain

import "testing"

// TestOverdueForeshadow 验证逾期判定：deadline>0 且 current>=deadline 且未回收。
func TestOverdueForeshadow(t *testing.T) {
	entries := []ForeshadowEntry{
		{ID: "f1", Status: "planted", Deadline: 10},  // 逾期（current=10）
		{ID: "f2", Status: "advanced", Deadline: 11}, // 未到期
		{ID: "f3", Status: "resolved", Deadline: 5},  // 已回收，豁免
		{ID: "f4", Status: "planted"},                // 无 deadline，豁免
		{ID: "f5", Status: "planted", Deadline: 3},   // 逾期
	}
	got := OverdueForeshadow(entries, 10)
	if len(got) != 2 {
		t.Fatalf("overdue = %d 条, want 2: %+v", len(got), got)
	}
	if got[0].ID != "f1" || got[1].ID != "f5" {
		t.Fatalf("overdue ids = %s,%s, want f1,f5", got[0].ID, got[1].ID)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/domain/ -run TestOverdueForeshadow -v`
Expected: FAIL（`ForeshadowEntry` 无 Deadline 字段 / `OverdueForeshadow` 未定义，编译错误）

- [ ] **Step 3: 实现**

`internal/domain/review.go` 中 `ForeshadowEntry` 与 `ForeshadowUpdate` 各加一个字段，并追加帮助函数：

```go
// ForeshadowEntry 伏笔条目。
type ForeshadowEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	PlantedAt   int    `json:"planted_at"`
	Status      string `json:"status"` // planted / advanced / resolved
	ResolvedAt  int    `json:"resolved_at,omitempty"`
	Deadline    int    `json:"deadline,omitempty"` // 建议回收章号（0=未设置；plant 时设置，advance 时可顺延）
}

// ForeshadowUpdate 伏笔增量操作。
type ForeshadowUpdate struct {
	ID          string `json:"id"`
	Action      string `json:"action"` // plant / advance / resolve
	Description string `json:"description,omitempty"`
	Deadline    int    `json:"deadline,omitempty"` // 建议回收章号（可选；plant 设置 / advance 顺延）
}

// OverdueForeshadow 返回已过建议回收章仍未回收的伏笔。
// 判定：Deadline>0 且 current >= Deadline 且 Status != resolved。
func OverdueForeshadow(entries []ForeshadowEntry, current int) []ForeshadowEntry {
	var out []ForeshadowEntry
	for _, e := range entries {
		if e.Status == "resolved" || e.Deadline <= 0 {
			continue
		}
		if current >= e.Deadline {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/domain/ -run TestOverdueForeshadow -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/domain/review.go internal/domain/review_test.go
git commit -m "feat(domain): 伏笔条目增加 deadline 建议回收章 + 逾期判定函数"
```

### Task 2: store — UpdateForeshadow 写入 deadline 并返回未知 ID

**Files:**
- Modify: `internal/store/world.go:97-136`
- Modify: `internal/tools/commit_chapter.go:213-217`（调用点签名适配，本 Task 仅保编译，事实透传在 Task 3）
- Test: `internal/store/world_test.go:106-111`（现有调用点适配）+ 新增用例

- [ ] **Step 1: 写失败测试**

在 `internal/store/world_test.go` 追加：

```go
// TestUpdateForeshadow_DeadlineAndUnknownIDs 验证 deadline 写入/顺延与未知 ID 返回。
func TestUpdateForeshadow_DeadlineAndUnknownIDs(t *testing.T) {
	s := NewStore(t.TempDir())
	// plant 带 deadline
	unknown, err := s.World.UpdateForeshadow(1, []domain.ForeshadowUpdate{
		{ID: "f1", Action: "plant", Description: "神秘玉佩", Deadline: 20},
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("plant: unknown=%v err=%v", unknown, err)
	}
	// advance 顺延 deadline + 一条未知 ID
	unknown, err = s.World.UpdateForeshadow(5, []domain.ForeshadowUpdate{
		{ID: "f1", Action: "advance", Deadline: 30},
		{ID: "ghost", Action: "resolve"},
	})
	if err != nil {
		t.Fatalf("advance err=%v", err)
	}
	if len(unknown) != 1 || unknown[0] != "ghost" {
		t.Fatalf("unknown = %v, want [ghost]", unknown)
	}
	entries, _ := s.World.LoadForeshadowLedger()
	if len(entries) != 1 || entries[0].Deadline != 30 || entries[0].Status != "advanced" {
		t.Fatalf("entries = %+v, want f1 deadline=30 advanced", entries)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run TestUpdateForeshadow_DeadlineAndUnknownIDs -v`
Expected: FAIL（编译错误：UpdateForeshadow 只返回一个值）

- [ ] **Step 3: 实现**

`internal/store/world.go` 替换 `UpdateForeshadow`：

```go
// UpdateForeshadow 批量应用伏笔增量操作。
// 返回 advance/resolve 引用了不存在 ID 的列表（未知 ID 静默丢弃曾导致台账漂移不可见，
// 现作为事实返回给调用方透传，不阻断）。
func (s *WorldStore) UpdateForeshadow(chapter int, updates []domain.ForeshadowUpdate) ([]string, error) {
	var unknown []string
	err := s.io.WithWriteLock(func() error {
		var entries []domain.ForeshadowEntry
		if err := s.io.ReadJSONUnlocked("foreshadow_ledger.json", &entries); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(entries))
		for i, e := range entries {
			idx[e.ID] = i
		}
		for _, u := range updates {
			switch u.Action {
			case "plant":
				idx[u.ID] = len(entries)
				entries = append(entries, domain.ForeshadowEntry{
					ID:          u.ID,
					Description: u.Description,
					PlantedAt:   chapter,
					Status:      "planted",
					Deadline:    u.Deadline,
				})
			case "advance":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "advanced"
					if u.Deadline > 0 {
						entries[i].Deadline = u.Deadline // 允许顺延
					}
				} else {
					unknown = append(unknown, u.ID)
				}
			case "resolve":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "resolved"
					entries[i].ResolvedAt = chapter
				} else {
					unknown = append(unknown, u.ID)
				}
			}
		}
		if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(entries))
	})
	return unknown, err
}
```

同步修两处现有调用点保编译：

1. `internal/store/world_test.go:106、111` 两处 `_ = s.World.UpdateForeshadow(...)` 改为 `_, _ = s.World.UpdateForeshadow(...)`。
2. `internal/tools/commit_chapter.go:213-217` 改为（unknown 的透传在 Task 3 完成，此处先接住）：

```go
	var foreshadowUnknown []string
	if len(a.ForeshadowUpdates) > 0 {
		unknown, err := t.store.World.UpdateForeshadow(a.Chapter, a.ForeshadowUpdates)
		if err != nil {
			return nil, fmt.Errorf("update foreshadow: %w: %w", errs.ErrStoreWrite, err)
		}
		foreshadowUnknown = unknown
	}
	_ = foreshadowUnknown // Task 3 透传进 commitOutput 后删除此行
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/store/ ./internal/tools/ -v -run TestUpdateForeshadow`
Expected: PASS；再跑 `go build ./...` Expected: 无错误

- [ ] **Step 5: 提交**

```bash
git add internal/store/world.go internal/store/world_test.go internal/tools/commit_chapter.go
git commit -m "feat(store): UpdateForeshadow 支持 deadline 写入/顺延并返回未知 ID 事实"
```

### Task 3: tools — commit_chapter 透传伏笔事实（unknown IDs + 逾期清单）

**Files:**
- Modify: `internal/tools/commit_chapter.go`（Schema 的 foreshadowSchema、commitOutput、Execute 末尾）
- Test: `internal/tools/commit_chapter_foreshadow_test.go`（新建）

- [ ] **Step 1: 先读现有测试固件**

打开 `internal/tools/` 下任一 commit_chapter 相关测试（`Glob: internal/tools/commit_chapter*_test.go`），确认构造 commit 前置状态（progress init / 草稿落盘）的现有 helper 写法。下面测试代码按"无专用 helper"假设编写，若已有 helper（如 `newTestStore`），改用之。

- [ ] **Step 2: 写失败测试**

新建 `internal/tools/commit_chapter_foreshadow_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

// TestCommitChapter_ForeshadowFacts 验证 commit 返回未知伏笔 ID 与逾期伏笔事实。
func TestCommitChapter_ForeshadowFacts(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Progress.Init("测试书", 10); err != nil {
		t.Fatal(err)
	}
	// 预埋一条 deadline=2 的伏笔（提交第 3 章时应逾期）
	if _, err := s.World.UpdateForeshadow(1, []domain.ForeshadowUpdate{
		{ID: "f-old", Action: "plant", Description: "旧伏笔", Deadline: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(3, "正文内容……"); err != nil {
		t.Fatal(err)
	}

	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    3,
		"summary":    "摘要",
		"characters": []string{"林尘"},
		"key_events": []string{"事件"},
		"foreshadow_updates": []map[string]any{
			{"id": "ghost", "action": "advance"}, // 未知 ID
		},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out struct {
		ForeshadowUnknownIDs []string                 `json:"foreshadow_unknown_ids"`
		ForeshadowOverdue    []domain.ForeshadowEntry `json:"foreshadow_overdue"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ForeshadowUnknownIDs) != 1 || out.ForeshadowUnknownIDs[0] != "ghost" {
		t.Fatalf("unknown_ids = %v, want [ghost]", out.ForeshadowUnknownIDs)
	}
	if len(out.ForeshadowOverdue) != 1 || out.ForeshadowOverdue[0].ID != "f-old" {
		t.Fatalf("overdue = %+v, want [f-old]", out.ForeshadowOverdue)
	}
}
```

注：若 `Progress.Init` 后直接 commit 第 3 章会被 `ValidateChapterWork` 拦截（顺序约束），按现有 commit 测试固件先依次完成第 1、2 章（或使用其既有跳号手段）调整前置；断言部分不变。

- [ ] **Step 3: 运行确认失败**

Run: `go test ./internal/tools/ -run TestCommitChapter_ForeshadowFacts -v`
Expected: FAIL（`foreshadow_unknown_ids` 等字段为空）

- [ ] **Step 4: 实现**

`internal/tools/commit_chapter.go` 三处修改：

(a) `commitOutput` 扩展：

```go
type commitOutput struct {
	domain.CommitResult
	RuleViolations       []rules.Violation        `json:"rule_violations,omitempty"`
	ForeshadowUnknownIDs []string                 `json:"foreshadow_unknown_ids,omitempty"`
	ForeshadowOverdue    []domain.ForeshadowEntry `json:"foreshadow_overdue,omitempty"`
}
```

(b) Schema 中 `foreshadowSchema` 增加 deadline 属性：

```go
	foreshadowSchema := schema.Object(
		schema.Property("id", schema.String("伏笔 ID")).Required(),
		schema.Property("action", schema.Enum("操作", "plant", "advance", "resolve")).Required(),
		schema.Property("description", schema.String("伏笔描述（仅 plant 时必需）")),
		schema.Property("deadline", schema.Int("建议回收章号（可选；plant 时设置预期回收点，advance 时可顺延）")),
	)
```

(c) Execute 末尾（第 11 步机械规则检查处）删除 Task 2 的 `_ = foreshadowUnknown` 占位行，并把返回改为：

```go
	// 11. 机械规则检查（仅返事实，不阻断；rulesOpts 未配置时返 nil）
	violations := t.checkRules(content, wordCount)

	// 12. 伏笔事实：逾期清单（deadline 已过仍未回收）。读失败不阻断 commit，仅缺该事实。
	var overdueFs []domain.ForeshadowEntry
	if active, ferr := t.store.World.LoadActiveForeshadow(); ferr == nil {
		overdueFs = domain.OverdueForeshadow(active, a.Chapter)
	}
	return json.Marshal(commitOutput{
		CommitResult:         result,
		RuleViolations:       violations,
		ForeshadowUnknownIDs: foreshadowUnknown,
		ForeshadowOverdue:    overdueFs,
	})
```

- [ ] **Step 5: 运行确认通过**

Run: `go test ./internal/tools/ -run TestCommitChapter_ForeshadowFacts -v` → PASS
Run: `go test ./internal/tools/` → 全部 PASS（确认无回归）

- [ ] **Step 6: 提交**

```bash
git add internal/tools/commit_chapter.go internal/tools/commit_chapter_foreshadow_test.go
git commit -m "feat(tools): commit_chapter 返回伏笔未知 ID 与逾期事实，schema 支持 deadline"
```

### Task 4: diag — ForeshadowOverdue 诊断规则

**Files:**
- Modify: `internal/diag/rules_planning.go`（追加规则）
- Modify: `internal/diag/diag.go:40-44`（注册到 allRules 的 Planning 段）
- Test: `internal/diag/rules_planning_test.go`（追加用例，沿用现有测试的 Snapshot 构造方式）

- [ ] **Step 1: 写失败测试**

在 `internal/diag/rules_planning_test.go` 追加（Snapshot/Progress 构造参照同文件 StaleForeshadow 现有测试）：

```go
// TestForeshadowOverdue 验证 deadline 逾期检测。
func TestForeshadowOverdue(t *testing.T) {
	snap := &Snapshot{
		Progress: &domain.Progress{CompletedChapters: []int{1, 2, 3, 4, 5}},
		Foreshadow: []domain.ForeshadowEntry{
			{ID: "f1", Status: "planted", PlantedAt: 1, Deadline: 4},  // 逾期
			{ID: "f2", Status: "advanced", PlantedAt: 1, Deadline: 9}, // 未到期
			{ID: "f3", Status: "resolved", PlantedAt: 1, Deadline: 2}, // 已回收
		},
	}
	got := ForeshadowOverdue(snap)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Rule != "ForeshadowOverdue" || !strings.Contains(got[0].Evidence, "f1") {
		t.Fatalf("finding = %+v", got[0])
	}
	// 无 deadline 数据时不报
	snap.Foreshadow = []domain.ForeshadowEntry{{ID: "x", Status: "planted"}}
	if got := ForeshadowOverdue(snap); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}
```

注：`snap.LatestCompleted()` 若依赖 Progress 的其他字段，按 `internal/diag/snapshot.go` 中该方法实际实现调整构造。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/diag/ -run TestForeshadowOverdue -v`
Expected: FAIL（ForeshadowOverdue 未定义）

- [ ] **Step 3: 实现**

`internal/diag/rules_planning.go` 追加：

```go
// ForeshadowOverdue 检测已过建议回收章（deadline）仍未回收的伏笔。
// 与 StaleForeshadow 互补：前者查"停滞"（无推进），本规则查"逾期"（过了约定回收点）。
func ForeshadowOverdue(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Foreshadow) == 0 {
		return nil
	}
	latest := snap.LatestCompleted()
	overdue := domain.OverdueForeshadow(snap.Foreshadow, latest)
	if len(overdue) == 0 {
		return nil
	}
	parts := make([]string, 0, len(overdue))
	for _, f := range overdue {
		parts = append(parts, fmt.Sprintf("%s(截止ch%d,现ch%d)", f.ID, f.Deadline, latest))
	}
	return []Finding{{
		Rule:       "ForeshadowOverdue",
		Category:   CatPlanning,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "context.foreshadow",
		Title:      fmt.Sprintf("伏笔逾期: %d 条已过建议回收章未回收", len(overdue)),
		Evidence:   strings.Join(parts, "; "),
		Suggestion: "提醒 Writer 在近期章节回收，或由 Architect 评估后顺延 deadline（commit 的 foreshadow_updates advance 操作携带新 deadline）。",
	}}
}
```

`internal/diag/diag.go` allRules 的 Planning 段，在 `StaleForeshadow,` 之后插入 `ForeshadowOverdue,`。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/diag/ -v` Expected: 全部 PASS

- [ ] **Step 5: Phase 1 收尾验证 + 提交**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全部通过

```bash
git add internal/diag/rules_planning.go internal/diag/rules_planning_test.go internal/diag/diag.go
git commit -m "feat(diag): 新增 ForeshadowOverdue 伏笔逾期诊断规则"
```

---

## Phase 2 — A2 角色硬状态约束（死亡角色出场机械检查）

现状：`check_consistency` 不加载 state_changes；commit 对"已死角色出场"无任何机械信号；诊断有 GhostCharacter（消失）但无"死人出场"。

### Task 5: domain — 状态折叠与死亡判定纯函数

**Files:**
- Modify: `internal/domain/tracking.go`
- Test: `internal/domain/tracking_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `internal/domain/tracking_test.go`：

```go
package domain

import "testing"

func TestIsDeadValue(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"死亡", true}, {"战死沙场", true}, {"已死", true}, {"身亡", true},
		{"假死脱身", false}, {"濒死", false}, {"重伤垂死", false},
		{"健在", false}, {"重伤", false}, {"诈死", false},
	}
	for _, c := range cases {
		if got := IsDeadValue(c.v); got != c.want {
			t.Errorf("IsDeadValue(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// TestDeadEntities 验证：取最新 status、死亡章早于 current 才算、复活豁免、同章死亡豁免。
func TestDeadEntities(t *testing.T) {
	changes := []StateChange{
		{Chapter: 3, Entity: "甲", Field: "status", NewValue: "死亡"},
		{Chapter: 2, Entity: "乙", Field: "status", NewValue: "死亡"},
		{Chapter: 4, Entity: "乙", Field: "status", NewValue: "复活归来"}, // 最新非死亡 → 豁免
		{Chapter: 5, Entity: "丙", Field: "status", NewValue: "战死"},    // 同章死亡 → 豁免（current=5）
		{Chapter: 1, Entity: "丁", Field: "realm", NewValue: "金丹"},     // 非 status → 忽略
	}
	dead := DeadEntities(changes, 5)
	if len(dead) != 1 {
		t.Fatalf("dead = %v, want 仅 甲", dead)
	}
	if ch, ok := dead["甲"]; !ok || ch != 3 {
		t.Fatalf("dead[甲] = %d,%v, want 3,true", ch, ok)
	}
}

func TestFoldStateChanges(t *testing.T) {
	changes := []StateChange{
		{Chapter: 1, Entity: "甲", Field: "realm", NewValue: "筑基"},
		{Chapter: 3, Entity: "甲", Field: "realm", NewValue: "金丹"},
		{Chapter: 2, Entity: "甲", Field: "location", NewValue: "青云山"},
	}
	folded := FoldStateChanges(changes)
	if folded["甲"]["realm"].NewValue != "金丹" || folded["甲"]["location"].NewValue != "青云山" {
		t.Fatalf("folded = %+v", folded["甲"])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/domain/ -run "TestIsDeadValue|TestDeadEntities|TestFoldStateChanges" -v`
Expected: FAIL（编译错误，函数未定义）

- [ ] **Step 3: 实现**

`internal/domain/tracking.go` 追加（import "strings"）：

```go
// deadMarkers 判定死亡状态的标记词；deadExclusions 排除"假死/濒死"等非死亡语义。
// 机械匹配采用保守策略：排除词优先，宁可漏报不误报（漏报由 editor 七维评审兜底）。
var deadMarkers = []string{"死亡", "已死", "阵亡", "身亡", "殒命", "战死", "病逝", "坠亡", "处决", "气绝"}
var deadExclusions = []string{"假死", "濒死", "诈死", "未死", "不死", "垂死", "半死", "九死", "求死", "找死"}

// IsDeadValue 机械判断 status 值是否表示死亡。
func IsDeadValue(v string) bool {
	for _, ex := range deadExclusions {
		if strings.Contains(v, ex) {
			return false
		}
	}
	for _, m := range deadMarkers {
		if strings.Contains(v, m) {
			return true
		}
	}
	return false
}

// FoldStateChanges 折叠状态变化：实体 → 属性 → 最新一条记录。
// 切片顺序即时间顺序（AppendStateChanges 仅追加），后者覆盖前者。
func FoldStateChanges(changes []StateChange) map[string]map[string]StateChange {
	out := make(map[string]map[string]StateChange)
	for _, c := range changes {
		if out[c.Entity] == nil {
			out[c.Entity] = make(map[string]StateChange)
		}
		out[c.Entity][c.Field] = c
	}
	return out
}

// DeadEntities 返回"最新 status 为死亡、且死亡记录早于 currentChapter"的实体 → 死亡记录章节。
// 同章死亡不算违规（角色可在本章出场后死亡）；后续复活（最新 status 非死亡）自动豁免。
func DeadEntities(changes []StateChange, currentChapter int) map[string]int {
	out := make(map[string]int)
	for entity, fields := range FoldStateChanges(changes) {
		c, ok := fields["status"]
		if !ok {
			continue
		}
		if IsDeadValue(c.NewValue) && c.Chapter < currentChapter {
			out[entity] = c.Chapter
		}
	}
	return out
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/domain/ -v` Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/domain/tracking.go internal/domain/tracking_test.go
git commit -m "feat(domain): 状态变化折叠 + 死亡实体机械判定纯函数"
```

### Task 6: tools — check_consistency 返回实体状态对照数据

**Files:**
- Modify: `internal/tools/check_consistency.go:64-87`
- Test: `internal/tools/check_consistency_test.go`（追加或新建）

- [ ] **Step 1: 写失败测试**

```go
// TestCheckConsistency_EntityStates 验证返回实体最新状态与死亡名单。
func TestCheckConsistency_EntityStates(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Drafts.SaveDraft(5, "正文"); err != nil {
		t.Fatal(err)
	}
	if err := s.World.AppendStateChanges([]domain.StateChange{
		{Chapter: 3, Entity: "甲", Field: "status", NewValue: "死亡"},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := NewCheckConsistencyTool(s).Execute(context.Background(),
		json.RawMessage(`{"chapter":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		EntityStates map[string]map[string]domain.StateChange `json:"entity_states"`
		DeadEntities map[string]int                           `json:"dead_entities"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.EntityStates["甲"]["status"].NewValue != "死亡" {
		t.Fatalf("entity_states = %+v", out.EntityStates)
	}
	if out.DeadEntities["甲"] != 3 {
		t.Fatalf("dead_entities = %+v, want 甲:3", out.DeadEntities)
	}
}
```

（import 对齐文件内现有测试；无现有测试文件则新建，imports: context/encoding/json/testing + domain + store）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/tools/ -run TestCheckConsistency_EntityStates -v`
Expected: FAIL（entity_states 缺失）

- [ ] **Step 3: 实现**

`internal/tools/check_consistency.go` 在 `recent_summaries` 加载之后、checkpoint 之前插入：

```go
	// 实体状态对照：最新状态快照 + 死亡名单（机械事实，Writer 自行对照本章出场角色）
	if changes, _ := t.store.World.LoadStateChanges(); len(changes) > 0 {
		result["entity_states"] = domain.FoldStateChanges(changes)
		if dead := domain.DeadEntities(changes, a.Chapter); len(dead) > 0 {
			result["dead_entities"] = dead
		}
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/tools/ -run TestCheckConsistency -v` Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/tools/check_consistency.go internal/tools/check_consistency_test.go
git commit -m "feat(tools): check_consistency 返回实体最新状态与死亡名单对照数据"
```

### Task 7: tools — commit_chapter 死亡角色出场事实告警

**Files:**
- Modify: `internal/tools/commit_chapter.go`（commitOutput + Execute 4c 步 + 别名 helper）
- Test: `internal/tools/commit_chapter_foreshadow_test.go`（同文件追加，或新建 commit_chapter_character_test.go）

- [ ] **Step 1: 写失败测试**

```go
// TestCommitChapter_DeadCharacterViolation 验证已死角色出场返回 character_violations。
func TestCommitChapter_DeadCharacterViolation(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Progress.Init("测试书", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.World.AppendStateChanges([]domain.StateChange{
		{Chapter: 1, Entity: "王老五", Field: "status", NewValue: "死亡"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(2, "正文"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "摘要",
		"characters": []string{"王老五"},
		"key_events": []string{"事件"},
	})
	raw, err := NewCommitChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		CharacterViolations []string `json:"character_violations"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.CharacterViolations) != 1 {
		t.Fatalf("character_violations = %v, want 1 条", out.CharacterViolations)
	}
}
```

（与 Task 3 同样的前置顺序注意：若需先完成第 1 章，按现有固件补。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/tools/ -run TestCommitChapter_DeadCharacterViolation -v`
Expected: FAIL

- [ ] **Step 3: 实现**

(a) `commitOutput` 追加字段：

```go
	CharacterViolations  []string                 `json:"character_violations,omitempty"`
```

(b) Execute 中第 4b 步（配角名册）之后插入 4c：

```go
	// 4c. 角色硬状态对照：本章出场角色若最新状态为死亡且死于更早章节，返回事实告警。
	// 仅返事实不阻断（铁律一）：误报场景（闪回/回忆）由 editor 语义评审豁免。
	// 注意本步在 4 之后执行：本章 state_changes 已落盘，"本章复活"会使最新状态非死亡而自动豁免。
	var characterViolations []string
	if len(a.Characters) > 0 {
		if changes, _ := t.store.World.LoadStateChanges(); len(changes) > 0 {
			if dead := domain.DeadEntities(changes, a.Chapter); len(dead) > 0 {
				alias := loadAliasToCanonical(t.store)
				for _, name := range a.Characters {
					canon := name
					if c, ok := alias[name]; ok {
						canon = c
					}
					if ch, ok := dead[canon]; ok {
						characterViolations = append(characterViolations,
							fmt.Sprintf("角色「%s」已于第 %d 章记录死亡，本章仍出场（若为闪回/复活请补 state_changes 修正）", canon, ch))
					}
				}
			}
		}
	}
```

(c) 文件末尾追加 helper：

```go
// loadAliasToCanonical 构建 别名→正名 映射；加载失败返回 nil（按原名匹配）。
func loadAliasToCanonical(s *store.Store) map[string]string {
	chars, err := s.Characters.Load()
	if err != nil {
		return nil
	}
	m := make(map[string]string)
	for _, c := range chars {
		for _, alias := range c.Aliases {
			if alias != "" && c.Name != "" {
				m[alias] = c.Name
			}
		}
	}
	return m
}
```

(d) 末尾 marshal 加入 `CharacterViolations: characterViolations,`。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/tools/ -v` Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/tools/commit_chapter.go internal/tools/commit_chapter_foreshadow_test.go
git commit -m "feat(tools): commit_chapter 死亡角色出场机械告警（character_violations 事实）"
```

### Task 8: diag — DeadCharacterAppears 诊断规则

**Files:**
- Modify: `internal/diag/rules_context.go`（GhostCharacter 所在文件；若实际在别处，与 GhostCharacter 同文件追加）
- Modify: `internal/diag/diag.go:45-48`（注册到 Context 段）
- Test: 同包对应测试文件追加

- [ ] **Step 1: 写失败测试**

```go
// TestDeadCharacterAppears 验证回溯检测：死亡后章节摘要仍出场。
func TestDeadCharacterAppears(t *testing.T) {
	snap := &Snapshot{
		StateChanges: []domain.StateChange{
			{Chapter: 2, Entity: "王老五", Field: "status", NewValue: "战死"},
		},
		Summaries: map[int]*domain.ChapterSummary{
			1: {Chapter: 1, Characters: []string{"王老五"}}, // 死前出场 → 正常
			4: {Chapter: 4, Characters: []string{"王老五"}}, // 死后出场 → 违规
		},
	}
	got := DeadCharacterAppears(snap)
	if len(got) != 1 || got[0].Rule != "DeadCharacterAppears" {
		t.Fatalf("findings = %+v, want 1 条", got)
	}
	if !strings.Contains(got[0].Evidence, "ch4") {
		t.Fatalf("evidence = %q, 应指向 ch4", got[0].Evidence)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/diag/ -run TestDeadCharacterAppears -v` → FAIL

- [ ] **Step 3: 实现**

与 GhostCharacter 同文件追加（imports 补 sort）：

```go
// DeadCharacterAppears 检测"已记录死亡的角色在更晚章节仍出场"。
// 出场依据章节摘要的 Characters 名单；死亡依据 state_changes 中该章之前最后一条 status。
// 复活剧情（死后又有非死亡 status 记录）自动豁免。
func DeadCharacterAppears(snap *Snapshot) []Finding {
	if len(snap.StateChanges) == 0 || len(snap.Summaries) == 0 {
		return nil
	}
	// 别名 → 正名
	alias := make(map[string]string)
	for _, c := range snap.Characters {
		for _, a := range c.Aliases {
			alias[a] = c.Name
		}
	}
	// 每个实体按时间序的 status 变化（切片顺序即追加顺序）
	statusSeq := make(map[string][]domain.StateChange)
	for _, sc := range snap.StateChanges {
		if sc.Field == "status" {
			statusSeq[sc.Entity] = append(statusSeq[sc.Entity], sc)
		}
	}
	var hits []string
	for ch, sum := range snap.Summaries {
		if sum == nil {
			continue
		}
		for _, name := range sum.Characters {
			canon := name
			if c, ok := alias[name]; ok {
				canon = c
			}
			var last *domain.StateChange
			for i := range statusSeq[canon] {
				if statusSeq[canon][i].Chapter < ch {
					last = &statusSeq[canon][i]
				}
			}
			if last != nil && domain.IsDeadValue(last.NewValue) {
				hits = append(hits, fmt.Sprintf("%s(死于ch%d,ch%d仍出场)", canon, last.Chapter, ch))
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Strings(hits) // map 遍历无序，排序保证输出与测试确定性
	return []Finding{{
		Rule:       "DeadCharacterAppears",
		Category:   CatContext,
		Severity:   SevCritical,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.characters",
		Title:      fmt.Sprintf("死亡角色出场: %d 处", len(hits)),
		Evidence:   strings.Join(hits, "; "),
		Suggestion: "核对相应章节：若为闪回/复活剧情，补 state_changes 修正状态；否则需要重写相关段落。",
	}}
}
```

`internal/diag/diag.go` allRules Context 段 `GhostCharacter,` 之后插入 `DeadCharacterAppears,`。

- [ ] **Step 4: Phase 2 收尾验证 + 提交**

Run: `go build ./... && go vet ./... && go test ./...` → 全部通过

```bash
git add internal/diag/
git commit -m "feat(diag): 新增 DeadCharacterAppears 死亡角色出场诊断规则"
```

---

## Phase 3 — B4 全书成本预算门禁

现状：`UsageTracker`（internal/host/usage.go）已有精确成本累计与持久化，但无任何预算概念；Dispatcher 每次派发无门禁。

设计：`budget.max_cost_usd` 配置上限 → Host 构造 `budgetGuard`（读 usage.Totals）→ 注入 `Dispatcher.SetGate` → 每次 Dispatch 前检查：达 80% 告警一次（warn 事件）；达 100% 拒绝派发 + error 事件 + 异步 `Abort()` 暂停。in-flight 子代理自然跑完，不强杀。暂停语义边界：超限暂停指停止派发新章；用户主动 Continue/干预仍会触发一轮 coordinator 响应成本，该轮结束后门禁再次拦截。

### Task 9: bootstrap — Budget 配置

**Files:**
- Modify: `internal/bootstrap/config.go:130-133`（Config 加字段）+ 同文件追加类型
- Modify: `internal/bootstrap/configfile.go:154-158`（mergeConfig 合并）
- Modify: `config.example.jsonc`
- Test: `internal/bootstrap/config_budget_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `internal/bootstrap/config_budget_test.go`：

```go
package bootstrap

import "testing"

func TestBudget_EnabledAndWarn(t *testing.T) {
	if (Budget{}).Enabled() {
		t.Fatal("零值不应启用")
	}
	b := Budget{MaxCostUSD: 10}
	if !b.Enabled() {
		t.Fatal("max_cost_usd>0 应启用")
	}
	if got := b.WarnUSD(); got != 8.0 { // 默认 0.8
		t.Fatalf("WarnUSD = %v, want 8.0", got)
	}
	if got := (Budget{MaxCostUSD: 10, WarnRatio: 0.5}).WarnUSD(); got != 5.0 {
		t.Fatalf("WarnUSD = %v, want 5.0", got)
	}
	if got := (Budget{MaxCostUSD: 10, WarnRatio: 1.5}).WarnUSD(); got != 8.0 { // 非法比例回默认
		t.Fatalf("WarnUSD = %v, want 8.0", got)
	}
}

func TestMergeConfig_Budget(t *testing.T) {
	got := mergeConfig(Config{}, Config{Budget: Budget{MaxCostUSD: 20}})
	if got.Budget.MaxCostUSD != 20 {
		t.Fatalf("overlay budget 未合并: %+v", got.Budget)
	}
	kept := mergeConfig(Config{Budget: Budget{MaxCostUSD: 5}}, Config{})
	if kept.Budget.MaxCostUSD != 5 {
		t.Fatalf("overlay 为空应保留 base: %+v", kept.Budget)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/bootstrap/ -run "TestBudget|TestMergeConfig_Budget" -v` → FAIL（编译错误）

- [ ] **Step 3: 实现**

`internal/bootstrap/config.go`：Config struct 的 `WritingContest` 字段之后加：

```go
	// 全书成本预算；MaxCostUSD<=0 视为未启用（完全向后兼容）。
	Budget Budget `json:"budget,omitempty"`
```

同文件 WritingContest 类型定义附近追加：

```go
// Budget 全书成本预算配置。累计成本（meta/usage.json 口径）达 WarnRatio 比例时告警，
// 达到 MaxCostUSD 后 Host 拒绝派发新指令并暂停运行（in-flight 子代理自然完成，不强杀）。
type Budget struct {
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"` // 美元上限；<=0 未启用
	WarnRatio  float64 `json:"warn_ratio,omitempty"`   // 告警阈值比例 (0,1)，默认 0.8
}

// Enabled 报告预算门禁是否启用。
func (b Budget) Enabled() bool { return b.MaxCostUSD > 0 }

// WarnUSD 返回告警线金额；WarnRatio 非法时回落默认 0.8。
func (b Budget) WarnUSD() float64 {
	r := b.WarnRatio
	if r <= 0 || r >= 1 {
		r = 0.8
	}
	return b.MaxCostUSD * r
}
```

`internal/bootstrap/configfile.go` mergeConfig 中 WritingContest 合并块之后加：

```go
	// Budget: overlay 配了上限则整体覆盖（与 WritingContest 同语义）。
	if overlay.Budget.MaxCostUSD > 0 {
		base.Budget = overlay.Budget
	}
```

`config.example.jsonc` 在 `writing_contest` 块后追加（保持文件现有注释风格）：

```jsonc
  // 全书成本预算（可选）：累计成本达上限的 80% 告警，达到上限后暂停创作。
  // 调高上限后重新启动即可恢复（进度与上下文均断点续写）。
  // "budget": {
  //   "max_cost_usd": 20.0,
  //   "warn_ratio": 0.8
  // },
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/bootstrap/ -v` → 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/bootstrap/config.go internal/bootstrap/configfile.go internal/bootstrap/config_budget_test.go config.example.jsonc
git commit -m "feat(config): 新增 budget 全书成本预算配置"
```

### Task 10: flow — Dispatcher 预算门禁注入点

**Files:**
- Modify: `internal/host/flow/dispatcher.go`
- Test: `internal/host/flow/dispatcher_test.go`（追加；fakeCoord 沿用该文件现有定义）

- [ ] **Step 1: 写失败测试**

在 `internal/host/flow/dispatcher_test.go` 追加（fakeCoord 类型与构造按文件内现有写法）：

```go
// TestDispatcher_GateBlocksDispatch 验证 gate 返回 false 时 Dispatch 整体短路（不读 store、不 FollowUp）。
func TestDispatcher_GateBlocksDispatch(t *testing.T) {
	fc := &fakeCoord{}
	d := NewDispatcher(nil, nil) // gate 在 LoadState 之前短路，nil store 不会被触碰
	d.coordinator = fc
	d.SetGate(func() bool { return false })
	d.Enable()
	d.Dispatch() // 若 gate 未生效会 panic（nil store）或 FollowUp
	if len(fc.msgs) != 0 {
		t.Fatalf("gate=false 时不应派发，got %d 条", len(fc.msgs))
	}
}
```

注：`NewDispatcher` 第一参数类型是 `*agentcore.Agent`（具体类型），fakeCoord 通过字段直赋 `d.coordinator`（dispatchCoordinator 接口）注入——与该测试文件现有用法一致；若现有测试用其他构造方式，沿用之。fakeCoord 若无 `msgs` 字段，按现有字段名（捕获 FollowUp 的切片）替换断言。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/host/flow/ -run TestDispatcher_GateBlocksDispatch -v` → FAIL（SetGate 未定义）

- [ ] **Step 3: 实现**

`internal/host/flow/dispatcher.go`：

(a) Dispatcher struct 加字段（contest 字段之后）：

```go
	// gate 预算/资源门禁；返回 false 时 Dispatch 整体短路（不派发新指令）。
	// 仅在 Host 装配期（Attach/Enable 之前）SetGate 一次，运行期只读，无并发写。
	gate func() bool
```

(b) 追加方法（SetContest 之后）：

```go
// SetGate 注入派发门禁；Host 在启用预算控制时装配期调用一次。
func (d *Dispatcher) SetGate(gate func() bool) { d.gate = gate }
```

(c) `Dispatch()` 函数体最顶部（LoadStateWithContest 之前）插入：

```go
	// 门禁：预算耗尽等场景拒绝派发新指令；in-flight 子代理自然完成。
	if d.gate != nil && !d.gate() {
		slog.Info("flow dispatch 被门禁拦截", "module", "host.flow")
		return
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/host/flow/ -v` → 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/host/flow/dispatcher.go internal/host/flow/dispatcher_test.go
git commit -m "feat(flow): Dispatcher 支持派发门禁注入（预算控制挂点）"
```

### Task 11: host — budgetGuard 实现与装配

**Files:**
- Create: `internal/host/budget.go`
- Create: `internal/host/budget_test.go`
- Modify: `internal/host/host.go:133-142`（New 中装配）

- [ ] **Step 1: 写失败测试**

新建 `internal/host/budget_test.go`：

```go
package host

import (
	"strings"
	"testing"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/internal/bootstrap"
)

// TestBudgetGuard_WarnOnceThenBlock 验证：80% 告警一次；超限拒绝 + abort 一次。
func TestBudgetGuard_WarnOnceThenBlock(t *testing.T) {
	cost := 0.0
	var events []Event
	aborted := make(chan struct{}, 4)
	g := newBudgetGuard(bootstrap.Budget{MaxCostUSD: 10},
		func() float64 { return cost },
		func(ev Event) { events = append(events, ev) },
		func() { aborted <- struct{}{} },
	)

	cost = 5.0
	if !g.Allow() || len(events) != 0 {
		t.Fatalf("50%% 应放行无事件: events=%v", events)
	}
	cost = 8.5
	if !g.Allow() {
		t.Fatal("85% 应放行")
	}
	if len(events) != 1 || events[0].Level != "warn" {
		t.Fatalf("应有一条 warn 告警: %+v", events)
	}
	if g.Allow(); len(events) != 1 {
		t.Fatal("告警只发一次")
	}
	cost = 10.5
	if g.Allow() {
		t.Fatal("超限应拒绝")
	}
	if len(events) != 2 || events[1].Level != "error" || !strings.Contains(events[1].Summary, "预算") {
		t.Fatalf("应有一条 error 事件: %+v", events)
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("超限应触发 abort")
	}
	if g.Allow() {
		t.Fatal("超限后持续拒绝")
	}
	if len(events) != 2 {
		t.Fatal("error 事件只发一次")
	}
	select {
	case <-aborted:
		t.Fatal("abort 只触发一次")
	case <-time.After(100 * time.Millisecond):
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/host/ -run TestBudgetGuard -v` → FAIL（newBudgetGuard 未定义）

- [ ] **Step 3: 实现 budget.go**

新建 `internal/host/budget.go`：

```go
package host

import (
	"fmt"
	"sync"
	"time"

	"github.com/Accelerator-mzq/ainovel-cli/internal/bootstrap"
)

// budgetGuard 在每次路由派发前检查累计成本（meta/usage.json 口径）是否超出预算。
// 注入 flow.Dispatcher.SetGate，Allow 可能被事件 goroutine 并发调用，需自带锁。
type budgetGuard struct {
	maxUSD  float64
	warnUSD float64
	costFn  func() float64 // 读累计成本（UsageTracker.Totals 第一返回值）
	emit    func(Event)
	abort   func() // 超限时暂停运行；异步调用避免与 coordinator 事件回调重入

	mu       sync.Mutex
	warned   bool
	exceeded bool
}

func newBudgetGuard(b bootstrap.Budget, costFn func() float64, emit func(Event), abort func()) *budgetGuard {
	return &budgetGuard{
		maxUSD:  b.MaxCostUSD,
		warnUSD: b.WarnUSD(),
		costFn:  costFn,
		emit:    emit,
		abort:   abort,
	}
}

// Allow 返回 false 表示预算耗尽，应拒绝派发新指令。
// 首次越过告警线 emit warn；首次超限 emit error 并异步暂停运行。
func (g *budgetGuard) Allow() bool {
	cost := g.costFn()
	g.mu.Lock()
	defer g.mu.Unlock()
	if cost >= g.maxUSD {
		if !g.exceeded {
			g.exceeded = true
			g.emit(Event{Time: time.Now(), Category: "SYSTEM", Level: "error",
				Summary: fmt.Sprintf("预算耗尽：累计成本 $%.2f ≥ 上限 $%.2f，已暂停创作。调高 budget.max_cost_usd 后重启可恢复", cost, g.maxUSD)})
			// 异步：Allow 在 Dispatcher 的事件回调里被调，同步 Abort 可能与 coordinator 内部锁重入
			go g.abort()
		}
		return false
	}
	if cost >= g.warnUSD && !g.warned {
		g.warned = true
		g.emit(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: fmt.Sprintf("预算告警：累计成本 $%.2f 已达上限 $%.2f 的 %.0f%%", cost, g.maxUSD, cost/g.maxUSD*100)})
	}
	return true
}
```

注：`Event` 结构体字段名（Time/Category/Summary/Level）以 `internal/host` 包内现有定义为准（host.go emitEvent 调用处可见）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/host/ -run TestBudgetGuard -v` → PASS

- [ ] **Step 5: 装配进 Host.New**

`internal/host/host.go` New 函数中，`h.routerDetach = h.router.Attach()`（约 142 行）之前插入：

```go
	// 预算门禁：累计成本接近上限告警，超限拒绝派发并暂停。
	if cfg.Budget.Enabled() {
		guard := newBudgetGuard(cfg.Budget,
			func() float64 { c, _, _, _, _ := usage.Totals(); return c },
			h.emitEvent,
			func() { h.Abort() },
		)
		h.router.SetGate(guard.Allow)
	}
```

- [ ] **Step 6: Phase 3 收尾验证 + 提交**

Run: `go build ./... && go vet ./... && go test ./...` → 全部通过

```bash
git add internal/host/budget.go internal/host/budget_test.go internal/host/host.go
git commit -m "feat(host): 全书成本预算门禁——80% 告警、超限暂停派发"
```

---

## Phase 4 — 竞稿两段式（synopsis 模式：先竞梗概，再写全文）

现状：N 个 persona 各写全章（token 成本 ≈ N×全章），judge 选 1，其余 N-1 份全文报废。两段式：候选阶段只写"梗概+开头试写"，judge 选优后由中选 persona 直接写全章。

关键复用：`draft_persona` 候选/润色两阶段判定（`v.Promoted && v.Winner==persona` → 写 draft.md）无需改动；StopGuard 切换依赖 task 文本含"润色"（`internal/agents/build.go:295-302`），两段式终稿任务文案必须保留该关键词。

### Task 12: 配置与事实管道（bootstrap.Mode → flow.ContestConfig.Synopsis → State.ContestSynopsis）

**Files:**
- Modify: `internal/bootstrap/config.go:135-166`
- Modify: `internal/host/flow/state.go:44-59`
- Modify: `internal/host/flow/router.go:55-67`（State 加字段）
- Modify: `internal/host/host.go:136-141`（SetContest 传参）
- Test: `internal/bootstrap/config_contest_test.go` + `internal/host/flow/` 现有 state 测试文件追加

- [ ] **Step 1: 写失败测试**

`internal/bootstrap/config_contest_test.go` 追加：

```go
// TestWritingContest_SynopsisMode 验证 mode 解析与 Normalize 保留。
func TestWritingContest_SynopsisMode(t *testing.T) {
	if (WritingContest{}).SynopsisMode() {
		t.Fatal("空 mode 不应为 synopsis")
	}
	if !(WritingContest{Mode: "synopsis"}).SynopsisMode() {
		t.Fatal("mode=synopsis 应生效")
	}
	if !(WritingContest{Mode: " Synopsis "}).SynopsisMode() {
		t.Fatal("应忽略大小写与空白")
	}
	got := WritingContest{Personas: []string{"乌贼", "土豆"}, Mode: "synopsis"}.Normalize()
	if !got.SynopsisMode() {
		t.Fatal("Normalize 丢失了 Mode")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/bootstrap/ -run TestWritingContest_SynopsisMode -v` → FAIL

- [ ] **Step 3: 实现**

(a) `internal/bootstrap/config.go` WritingContest 加字段与方法：

```go
	// Mode 竞稿模式：""/"full"（默认，候选写全章）或 "synopsis"（两段式：候选只写
	// 梗概+开头试写，中选后由该 persona 写全章。token 成本约降为 full 模式的 1/N）。
	Mode string `json:"mode,omitempty"`
```

```go
// SynopsisMode 报告是否启用两段式（梗概竞稿）。未知值按 full 处理。
func (w WritingContest) SynopsisMode() bool {
	return strings.EqualFold(strings.TrimSpace(w.Mode), "synopsis")
}
```

Normalize 返回值补 Mode：

```go
	return WritingContest{Personas: out, Judge: w.Judge, Concurrency: w.Concurrency, Mode: w.Mode}
```

(b) `internal/host/flow/state.go`：

```go
// ContestConfig 是 LoadStateWithContest 需要的竞稿静态配置（来自 bootstrap.Config 解析后的 slug 列表）。
type ContestConfig struct {
	Personas    []string // persona slug，顺序即写作顺序；len<2 视为未启用
	Concurrency bool     // 候选生成是否并发
	Synopsis    bool     // 两段式：候选只写梗概+开头，中选后写全章
}
```

LoadStateWithContest 中 `s.ContestConcurrent = cfg.Concurrency` 之后加：

```go
	s.ContestSynopsis = cfg.Synopsis // 两段式开关透传
```

(c) `internal/host/flow/router.go` State struct 的 `ContestConcurrent` 字段之后加：

```go
	ContestSynopsis      bool            // 两段式：候选为梗概+开头，终稿阶段才写全章
```

(d) `internal/host/host.go` SetContest 调用补参数：

```go
		h.router.SetContest(flow.ContestConfig{
			Personas:    persona.Slugs(wc.Personas), // persona slug 列表
			Concurrency: wc.Concurrency,             // 并发开关透传
			Synopsis:    wc.SynopsisMode(),          // 两段式开关透传
		})
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/bootstrap/ ./internal/host/flow/ -v && go build ./...` → PASS

- [ ] **Step 5: 提交**

```bash
git add internal/bootstrap/config.go internal/bootstrap/config_contest_test.go internal/host/flow/state.go internal/host/flow/router.go internal/host/host.go
git commit -m "feat(contest): writing_contest.mode=synopsis 配置与事实管道"
```

### Task 13: router — 两段式任务文案

**Files:**
- Modify: `internal/host/flow/router.go:199-273`（routeContest）
- Test: `internal/host/flow/router_test.go`（追加，State 构造沿用现有竞稿用例写法）

- [ ] **Step 1: 写失败测试**

`internal/host/flow/router_test.go` 追加：

```go
// TestRouteContest_SynopsisTasks 验证两段式三处任务文案。
func TestRouteContest_SynopsisTasks(t *testing.T) {
	base := State{
		Progress:        &domain.Progress{Phase: domain.PhaseWriting},
		ContestEnabled:  true,
		ContestChapter:  3,
		ContestSynopsis: true,
		Personas:        []string{"a", "b"},
		CandidatesReady: map[string]bool{},
		Abandoned:       map[string]bool{},
	}

	// 候选阶段：任务应为"候选梗概"
	inst := Route(base)
	if inst == nil || !strings.Contains(inst.Task, "候选梗概") {
		t.Fatalf("候选任务 = %+v, 应含 候选梗概", inst)
	}
	if strings.Contains(inst.Task, "候选稿") {
		t.Fatalf("synopsis 模式不应出现 候选稿: %q", inst.Task)
	}

	// judge 阶段
	s := base
	s.CandidatesReady = map[string]bool{"a": true, "b": true}
	inst = Route(s)
	if inst == nil || inst.Agent != "judge" || !strings.Contains(inst.Task, "候选梗概") {
		t.Fatalf("judge 任务 = %+v, 应含 候选梗概", inst)
	}

	// 终稿阶段：已提升 → 中选 writer 写全章，任务必须含"润色"（StopGuard 切换关键词）
	s.HasVerdict = true
	s.IsPromoted = true
	s.VerdictWinner = "a"
	s.VerdictRevisionNotes = "节奏再快些"
	inst = Route(s)
	if inst == nil || inst.Agent != "writer_a" {
		t.Fatalf("终稿任务 = %+v", inst)
	}
	if !strings.Contains(inst.Task, "全章正文") || !strings.Contains(inst.Task, "润色") {
		t.Fatalf("终稿任务文案 = %q, 应含 全章正文 与 润色", inst.Task)
	}
}
```

（import strings/domain 按文件现有 import 对齐；若现有竞稿测试有 State 构造 helper，沿用。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/host/flow/ -run TestRouteContest_SynopsisTasks -v` → FAIL

- [ ] **Step 3: 实现**

`internal/host/flow/router.go` routeContest 三处文案改为模式感知（整函数内逐处替换）：

候选任务（并发 batch 与串行两处共用）——在 `pending := PendingCandidates(s)` 之前定义：

```go
	// 两段式候选只写梗概+开头试写；full 模式写全章候选。
	candTask := fmt.Sprintf("写第 %d 章候选稿", ch)
	if s.ContestSynopsis {
		candTask = fmt.Sprintf("写第 %d 章候选梗概：先写 500-800 字情节梗概（含关键转折与章末钩子），再写本章开头约 300 字试写。不要写全章正文", ch)
	}
```

并发分支 `Task: fmt.Sprintf("写第 %d 章候选稿", ch)` → `Task: candTask`；串行分支同样替换。

judge 任务：

```go
	if !s.HasVerdict {
		form := "候选稿"
		if s.ContestSynopsis {
			form = "候选梗概"
		}
		return &Instruction{
			Agent:   "judge",
			Task:    fmt.Sprintf("评审第 %d 章的 %d 份%s，选优并给修改意见（save_verdict）", ch, nonAbandoned, form),
			Reason:  "竞稿：候选已齐，待选优",
			Chapter: ch,
		}
	}
```

终稿任务（第 4 步）：

```go
	// 4. 已提升 → 派中选 writer。full 模式润色已提升的候选全文；
	//    synopsis 模式按中选梗概直接写全章（draft.md 由本任务首次写入）。
	//    两种文案都必须含"润色"——build.go 的 StopGuardFactory 据此切到 WriterStopGuard（允许 commit）。
	if s.ContestSynopsis {
		return &Instruction{
			Agent:   "writer_" + s.VerdictWinner,
			Task:    fmt.Sprintf("你的梗概在第 %d 章竞稿胜出。按梗概写出全章正文，按评审意见润色后提交。评审意见：%s", ch, s.VerdictRevisionNotes),
			Reason:  fmt.Sprintf("竞稿(两段式)：%s 中选，写全章后提交", s.VerdictWinner),
			Chapter: ch,
		}
	}
	return &Instruction{
		Agent:   "writer_" + s.VerdictWinner,
		Task:    fmt.Sprintf("按选优意见润色并提交第 %d 章。选优意见：%s", ch, s.VerdictRevisionNotes),
		Reason:  fmt.Sprintf("竞稿：%s 中选，润色后提交", s.VerdictWinner),
		Chapter: ch,
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/host/flow/ -v` → 全部 PASS（含原 full 模式回归用例）

- [ ] **Step 5: 提交**

```bash
git add internal/host/flow/router.go internal/host/flow/router_test.go
git commit -m "feat(contest): routeContest 两段式任务文案（梗概候选/梗概评审/中选写全章）"
```

### Task 14: store + dispatcher — 两段式提升语义（只置标记不复制文件）

**Files:**
- Modify: `internal/store/contest.go`（新增 MarkVerdictPromoted）
- Modify: `internal/host/flow/dispatcher.go:206-221`（PromoteIfNeeded 分支）
- Test: `internal/store/contest_test.go`（追加）+ `internal/host/flow/dispatcher_test.go`（追加）

- [ ] **Step 1: 写失败测试**

`internal/store/contest_test.go` 追加（fixture 写法沿用该文件现有用例）：

```go
// TestMarkVerdictPromoted 验证仅置位不复制候选文件。
func TestMarkVerdictPromoted(t *testing.T) {
	s := NewStore(t.TempDir())
	// 无 verdict → 报错
	if err := s.Contest.MarkVerdictPromoted(3); err == nil {
		t.Fatal("无 verdict 应报错")
	}
	if err := s.Contest.SaveVerdict(domain.Verdict{Chapter: 3, Winner: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Contest.MarkVerdictPromoted(3); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Contest.LoadVerdict(3)
	if v == nil || !v.Promoted {
		t.Fatalf("verdict = %+v, want Promoted=true", v)
	}
	// 不应产生 draft.md（梗概不是正文）
	if content, _ := s.Drafts.LoadDraft(3); content != "" {
		t.Fatalf("synopsis 提升不应写 draft.md, got %q", content)
	}
	// 幂等
	if err := s.Contest.MarkVerdictPromoted(3); err != nil {
		t.Fatal("重复调用应幂等")
	}
}
```

注：`s.Drafts.LoadDraft` 方法名若实际为别名（如 LoadChapterContent 草稿分支），按 `internal/store/drafts.go` 实际只读草稿入口替换断言。

`internal/host/flow/dispatcher_test.go` 追加：

```go
// TestPromoteIfNeeded_Synopsis 验证两段式提升只置标记。
func TestPromoteIfNeeded_Synopsis(t *testing.T) {
	s := storepkg.NewStore(t.TempDir())
	_ = s.Contest.SaveVerdict(domain.Verdict{Chapter: 2, Winner: "a"})
	cfg := ContestConfig{Personas: []string{"a", "b"}, Synopsis: true}
	if !PromoteIfNeeded(s, cfg, 2) {
		t.Fatal("应提升成功")
	}
	v, _ := s.Contest.LoadVerdict(2)
	if v == nil || !v.Promoted {
		t.Fatalf("verdict = %+v", v)
	}
}
```

（import 别名 storepkg/domain 按该测试文件现有 import 对齐。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ ./internal/host/flow/ -run "TestMarkVerdictPromoted|TestPromoteIfNeeded_Synopsis" -v` → FAIL

- [ ] **Step 3: 实现**

`internal/store/contest.go` 追加：

```go
// MarkVerdictPromoted 仅置 verdict.Promoted=true，不复制候选文件。
// 两段式（梗概竞稿）用：中选的是梗概而非正文，draft.md 由中选 writer 在终稿阶段直接写入。
// 幂等：已提升时直接返回 nil。
func (s *ContestStore) MarkVerdictPromoted(chapter int) error {
	v, err := s.LoadVerdict(chapter)
	if err != nil {
		return fmt.Errorf("load verdict before mark promoted: %w", err)
	}
	if v == nil {
		return fmt.Errorf("chapter %d has no verdict; call SaveVerdict before MarkVerdictPromoted", chapter)
	}
	if v.Promoted {
		return nil
	}
	v.Promoted = true
	return s.SaveVerdict(*v)
}
```

`internal/host/flow/dispatcher.go` PromoteIfNeeded 在 `if err != nil || v == nil || v.Promoted { return false }` 之后插入：

```go
	if cfg.Synopsis {
		// 两段式：中选的是梗概不是正文，不复制候选槽；仅置 Promoted 标记，
		// draft.md 由中选 writer 在终稿任务里直接撰写（draft_persona 的润色分支）。
		if err := store.Contest.MarkVerdictPromoted(chapter); err != nil {
			slog.Warn("contest mark promoted failed", "module", "host.flow", "chapter", chapter, "err", err)
			return false
		}
		return true
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/store/ ./internal/host/flow/ -v` → 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/contest.go internal/store/contest_test.go internal/host/flow/dispatcher.go internal/host/flow/dispatcher_test.go
git commit -m "feat(contest): 两段式提升语义——只置 verdict 标记，全章由中选 writer 直写"
```

### Task 15: 配套文档与 judge 提示词说明

**Files:**
- Modify: `config.example.jsonc`（writing_contest 块加 mode 注释）
- Modify: `assets/prompts/judge.md`（追加候选形态说明）
- Modify: `README.md`（竞稿章节补一段两段式说明，位置在 L408-418 附近的竞稿流程描述后）

- [ ] **Step 1: config.example.jsonc**

`writing_contest` 块内（personas/concurrency 同级）追加注释行：

```jsonc
    // "mode": "synopsis",  // 两段式竞稿：候选只写梗概+开头，中选后写全章（成本约降为 1/N）；缺省 full 写全章
```

- [ ] **Step 2: assets/prompts/judge.md 末尾追加**

```markdown
## 候选形态

候选可能是「全章正文」（full 模式）或「梗概 + 开头试写」（两段式竞稿）。按 Host 任务说明中的形态评审：

- 梗概形态：评故事推进的合理性、关键转折的力度、章末钩子的设计、开头试写的文风质感。
- 正文形态：按全文品质综合评审。

两种形态都用 save_verdict 提交裁定，revision_notes 写给中选 writer 的成稿/润色指引。
```

- [ ] **Step 3: README.md**

在竞稿流程说明（"每章写作时：多 persona 各写一稿 → Judge 选优 → 润色提交"附近）追加：

```markdown
**两段式竞稿（成本优化）**：`writing_contest.mode: "synopsis"` 时候选阶段只写「500-800 字梗概 + 300 字开头试写」，Judge 在梗概层面选优，中选 persona 再写全章正文。全章 token 只花一份，竞稿成本约降为 full 模式的 1/N，文风与故事走向的差异在梗概阶段已可判别。
```

- [ ] **Step 4: 终验 + 提交**

Run: `go build ./... && go vet ./... && go test ./...` → 全部通过

```bash
git add config.example.jsonc assets/prompts/judge.md README.md
git commit -m "docs: 两段式竞稿配置说明与 judge 候选形态指引"
```

---

## 总验收清单

- [ ] `go build ./... && go vet ./... && go test ./...` 全绿（在干净工作树上）
- [ ] Phase 1：commit 返回 `foreshadow_unknown_ids` / `foreshadow_overdue`；`/diag` 出 ForeshadowOverdue
- [ ] Phase 2：commit 返回 `character_violations`；check_consistency 返回 `entity_states`/`dead_entities`；`/diag` 出 DeadCharacterAppears
- [ ] Phase 3：配 `budget.max_cost_usd` 后，80% warn 事件、超限 error 事件 + 运行暂停；不配完全无行为变化
- [ ] Phase 4：`mode:"synopsis"` 时路由文案三处切换、提升不复制文件；不配 mode 时 full 模式行为与现状逐字节一致（现有 router/dispatcher 测试回归通过）
- [ ] 四个 Phase 各自独立成串提交，可分别 revert
