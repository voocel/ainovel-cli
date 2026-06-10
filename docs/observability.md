# 观测手册

跑长篇小说时，怎么知道各项机制是不是真的在工作？

本文档不是把 diag 规则照抄一遍，而是面向**实际运行**：你跑到第 N 章了，应该打开哪个文件、看哪个字段、判断健康还是异常。

---

## 1. 通用排查流程

```
1. /diag                       # 自动诊断，看 Findings 区
2. cd output/{novel}/meta/     # 直接 cat 关键工件
3. cat meta/sessions/coordinator.jsonl | tail  # 看最近几轮 LLM 行为
```

`/diag` 覆盖不到的事实（包括本文档列出的"待补诊断"项），需要 step 2-3 手工查。

### 报 issue：脱敏诊断导出

每次 `/diag` 都会额外写出 `output/{novel}/meta/diag-export.md`——一份**已脱敏**的诊断（小说正文 / prompt / 思考已移除，仅保留行为骨架：工具名、错误串、重复次数、phase/flow、卡住的 step、日志错误分类）。遇到死循环 / 中断类问题，把这个文件贴到 GitHub issue 即可，维护者据此定位，无需用户的 `output/` 数据。

---

## 2. 关键工件速查表

按"出问题时最常见排查路径"排序：

| 工件 | 路径 | 看什么 | 健康 | 不健康 |
|---|---|---|---|---|
| 进度 | `meta/progress.json` | `phase` / `flow` / `completed_chapters` | phase 单调前进，flow 在合法集合内 | phase 倒退 / flow 卡在某状态 |
| 指南针 | `meta/compass.json` | `last_updated` 与最新章节差距 | gap < 15 章 | gap > 15 章（CompassDrift 命中） |
| 配角名册 | `meta/cast_ledger.json` | 条目数 / brief_role 填写率 / 名字一致性 | 见 §4 | 见 §4 |
| 伏笔台账 | `meta/foreshadow.json` | `status="planted"` 的最长停滞章数 | < 章数/3 | > 章数/3（StaleForeshadow 命中） |
| 大纲 | `meta/layered_outline.json` | 当前卷剩余未写章数 | 提前 1-2 章已展开 | 写到当前章但下一章无 outline（OutlineExhausted） |
| 角色档案 | `meta/characters.json` | 是否能在最近 N 章摘要里找到 core/important 角色 | 都能找到 | 缺席（GhostCharacter 命中） |
| 检查点 | `meta/checkpoints.jsonl` | 最近一行的 `step` 是否对应 progress | 一致 | 不一致（崩溃恢复未自愈） |
| Coordinator 会话 | `meta/sessions/coordinator.jsonl` | 最近 5-10 轮的 tool_call 模式 | 单轮快速推进 | 同一工具空调多次（卡死循环） |

---

## 3. 指南针（compass）观测

**修复时间**：2026-05-08（commit `fix: update_compass 工具自动填 last_updated`）

### 看什么

```bash
cat output/{novel}/meta/compass.json
```

字段语义：
- `ending_direction`：终局方向（应该和 `premise.md` "终局方向"段一致）
- `open_threads`：活跃长线（每卷边界由 architect 增删）
- `estimated_scale`：预估规模（如"4-6 卷"，每卷边界更新）
- `last_updated`：**工具自动填**为更新时的最大已完成章号（不再依赖 LLM 自填）

### 健康度判断

| 信号 | 判断 |
|---|---|
| `last_updated` 在 `[latest-15, latest]` 范围 | 健康 |
| `last_updated` 滞后 latest 超过 15 章 | architect 没在弧/卷边界更新——查 architect-long.md prompt |
| `last_updated == 0` | **本次修复前的脏数据**，下次 update_compass 会自愈 |
| `ending_direction` 和 premise.md "终局方向"段对不上 | architect 偷偷改了用户意图——记录下来，决定要不要冻结字段（设计议题，见 todo.md） |

### 怎么验证修复有效

跑长篇前后对比：
- **修复前**：跑 30+ 章后 `compass.last_updated` 大概率是 `0` 或某个早期章号
- **修复后**：每次 architect 调 `update_compass`，`last_updated` 都被工具层覆盖为当前 latest

---

## 4. 配角名册（cast_ledger）观测

**功能落地**：2026-05-08（commit `feat: 新增配角名册自动追踪次要角色`）

### 看什么

```bash
cat output/{novel}/meta/cast_ledger.json | jq 'length'                     # 条目总数
cat output/{novel}/meta/cast_ledger.json | jq '[.[] | select(.brief_role == "" or .brief_role == null)] | length'  # 缺 brief_role 数
cat output/{novel}/meta/cast_ledger.json | jq '[.[] | select(.appearance_count >= 3)] | length'   # 频繁出场（≥3 次）数
cat output/{novel}/meta/cast_ledger.json | jq 'sort_by(-.appearance_count) | .[:10]'  # 出场最多的 10 个
```

### 健康度判断

| 维度 | 健康 | 异常 | 应对 |
|---|---|---|---|
| **条目数 vs 已完成章数** | ledger 条目数 ≈ 已完成章数 × 0.3-0.6 | > 章数 × 0.8（过场角色被错误入册） | 查 writer.md 的 `cast_intros` 段是否够明确 |
| **brief_role 填写率** | 缺失 < 30% | 缺失 > 50% | Writer 漏填严重——prompt 引导不足 |
| **同名相似度** | 没有疑似同人多名 | 同时出现 "李X" / "老李" / "X掌柜" | LLM 名字漂移——prompt 加约束"用一致名字"或加用户 steer 合并工具 |
| **频繁出场角色** | `appearance_count >= 5` 的条目稀少 | 大量条目跨弧高频出场 | 该考虑升格到核心档案（阶段 3 升格通道） |
| **召回是否被消费** | Writer 写到旧角色时，commit_chapter 的 characters 字段里包含 ledger 已有名字 | Writer 重复发明同一个名字（出现"老周A"和"老周B"） | recent_cast 召回未被消费——检查 writer.md "配角连续性"段 |

### 数据流验证（端到端）

跑 5 章后：
1. `cat meta/cast_ledger.json` 应该不为空（除非每章都只用核心角色）
2. 如果 Writer 在第 1 章引入了"老周"：
   - `cast_ledger` 中应有 `老周` 条目，`appearance_count=1`
3. 如果第 5 章再写老周：
   - `老周.appearance_count=2`，`last_seen_chapter=5`
4. `meta/sessions/agents/writer-*.jsonl` 中第 5 章的 novel_context 返回值，应该在 `episodic_memory.recent_cast` 里看到老周
5. 如果上一步看到了但 Writer 没消费（写出来的老周和第 1 章对不上）—— 这是 prompt 问题

### 当前没有自动诊断（但 snapshot 已加载）

`diag.Snapshot.CastLedger` 已经在 `Load()` 里被读取，可以被规则直接消费——但当前还没写任何规则。验证仍靠上面的 `jq` 命令手工查。

后续如果要补诊断规则（候选）：
- `CastBriefRoleMissing`：缺失率 > 50% 告警
- `CastBloat`：条目数 > 章数 × 0.8 告警
- `CastPromotionCandidate`：appearance_count ≥ 5 且跨弧 → 建议升格

阈值不要现在拍——等长篇数据出来后，看真实分布再定。规则代码本身只需要 30-50 行。

---

## 5. Writer 是否在按预期工作

跑长篇时最关心的是 **Writer 真的在按 prompt 行事吗**。最直接的观测是 session log：

```bash
ls output/{novel}/meta/sessions/agents/    # 每个子代理一份 jsonl
tail -50 output/{novel}/meta/sessions/agents/writer-*.jsonl
```

看几个特定行为：

| 期望行为 | 在 jsonl 中体现 |
|---|---|
| Writer 看了 recent_cast | novel_context 工具返回值里 `episodic_memory.recent_cast` 字段非空 |
| Writer 在 commit_chapter 填了 cast_intros | tool_call 参数 `cast_intros` 数组非空（仅在引入新角色的章节） |
| Writer 用了相关章节推荐 | `read_chapter` 调用次数 > 1（默认 1 次，超过说明回查了） |
| Writer 没违反工具顺序 | tool_call 序列严格 `novel_context → read_chapter → plan_chapter → draft_chapter → check_consistency → commit_chapter` |

如果 jsonl 里看到 Writer 多次空调 novel_context、或 commit_chapter 之后又调其他工具——是 prompt 没收住。

---

## 6. 长跑场景红线

跑 100+ 章长篇时，下面任何一条命中就该停下来排查：

- [ ] CompassDrift 命中且持续 2 个弧未消除
- [ ] cast_ledger 条目数 > 已完成章数 × 0.8
- [ ] cast_ledger 中 brief_role 填写率 < 30%
- [ ] 同一角色出现疑似多名（"老李" / "李掌柜" 共存）
- [ ] Writer 写新章时不读 recent_cast 中已有的旧角色（重复发明）
- [ ] Coordinator session 中出现连续 ≥ 5 次空调 novel_context
- [ ] 任意章节 commit 后 `meta/checkpoints.jsonl` 没有对应 `commit_chapter` step

前 4 条是本次新机制的健康度；后 3 条是已有机制的稳定性。

---

## 7. 文档维护规范

**新增事实层工件时（新建一个 `meta/*.json` / `meta/*.jsonl`），同步：**

1. 在本文档 §2 加一行速查
2. 如果工件需要专项观测（不是简单的"存在/不存在"判断），加 §X 专题段
3. 如果想要自动诊断，在 `internal/diag/snapshot.go::Load` 中加载，并在 `internal/diag/rules_*.go` 加规则

**不要：**
- 不要把 `internal/diag/` 里所有规则照抄到本文档（那是规则参考，不是观测手册）
- 不要为每个机制都写诊断规则——阈值靠拍脑袋会错，先观察再补
