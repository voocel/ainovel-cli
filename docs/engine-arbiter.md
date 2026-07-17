# 控制面演进:Engine + Arbiter(移除 Coordinator 长循环)

> 状态(2026-07-14 v6):**代码实现完成**——Engine/Arbiter 已实施,Coordinator 及全部配套已删除(§十清单);端到端验证写完整书、失败裁定、僵局裁定、返工验收(hold+editor 时序)、boundary hold 即停、退出竞态干预保全与单许可单章节。三轮外部评审的阻断项全部处置(含 feedback 事实闭环、PendingSteer 崩溃保护、lifecycle 竞态)。
> **文档迁移已完成(2026-07-12)**:architecture.md 正文已全量重写为 Engine+Arbiter 现行架构(含新验证策略/目录/纪律);README、context-management、evaluation-system、observability、user-rules-runtime 的旧架构叙事已清理(仅保留标注的历史对照)。Coordinator 配置与会话兼容路径均已删除；Arbiter 当前刻意统一使用 Default 模型，不开放独立角色配置。
> **设计语义澄清(第四/五轮评审)**:① writer feedback 的消费点**就是**下一次结构操作(expand_arc/append_volume/update_compass 经 novel_context 参考后清空)——它是"对后续大纲的建议"(commit schema 原文),不是即时调度信号;弧中途的严重偏离走 editor 评审与用户干预通道。非分层书无结构操作,**commit 不落盘其 feedback**(避免永久无消费者的垃圾事实;返回值镜像保留供诊断)。② rule_violations 已闭环:commit 双路径落盘(**best-effort 质量元数据**,与章节提交非同级强一致——恰在 pending_commit 清除后崩溃会缺一条记录,可接受)→ novel_context(chapter=N) 注入 → editor 按 §机械检查映射消费。③ PendingSteer 崩溃保护是 **best-effort 单在途持久化**:首次持久化失败会显式停止裁定；裁定期、动作应用失败、正常退出/Abort 受保护;两个明确不保证的窗口——(a) 派单转入内存执行队列(e.next)后、worker 启动前的硬杀进程(毫秒级窗口,defer 不执行);(b) interMu 等待中的并发干预(尚未写入槽位)。用户在场可感知,重发成本秒级,不为此建持久化 intent/FIFO。④ 启动裁定失败不是死局(2026-07-12 真实故障补课:provider 账号失效致 plan_start 失败,恢复路径全部走不通):StartPrompt(输入事实)改为在裁定**之前**落盘;plan_start 从未完成时,引擎 planStartFallback 依据它现场补裁——首次裁定的重试不违反"恢复不重做已有裁定";补裁失败显式暂停回显,失败裁定的审计记录带 error 字段(DecisionRecord.Error)。
> 本文档保留为设计决策记录;当前架构见 README 架构节与 docs/engine-rfc.md。关联:docs/voice-layer.md(已实施)。

## 一、动机:被打补丁包围的过时假设

项目的 founding assumption——"一次 Prompt、一个常驻 LLM 长循环驱动整本书"——已过时。4 月 Hybrid 重构后,决策权实际在 `flow.Route` 手里,Coordinator 在主循环 90% 的调用只做**原样转发**。为"维持一个不该停的 LLM 会话"付出的补丁生态:

1. StopGuard + blockMessage 动态文案工程
2. Dispatcher 重复指令协议("第 N 次下达")
3. coordinator.md 行为规训(恢复特例 / 查询类必须同轮派单 / 不得用停机表达立场)
4. completePhaseGate / writerExpandedChapterGate
5. MaxTurns=100_000
6. FlowBoundaryHook
7. 完结分歧特例

**项目内部新子系统(import/simulation/cocreate/userrules)全部没走 Coordinator,已在用"Host 直接编排 + LLM 作函数"范式**——本方案把主流程统一到自己已验证的模式上。

## 二、目标形态

```
Entry
  ↓
Host(保留包名;内部新增 EngineLoop,不做纯机械改名)
  ├─ 读 Store → flow.Route → 直接运行 Worker
  ├─ 明确语义场景 → 调 Arbiter 函数
  └─ 事件投影 / 预算 / 停靠点 / 通知(现职responsibility保留)
  ↓
Workers(architect / writer / editor,内部自主,checkpoint-delta 守卫保留)
  ↓
Tools → Store(唯一事实源)
```

职责:**Route 管一切可查表的下一步;Arbiter 管边界清晰的语义判断;Worker 管开放式创作;Engine 执行决定、不参与文学判断;Observer/Diag 只观察。**

一句话概括终态:**一个串行确定性 Engine、三个自主 Worker、少数几个按需 Arbiter 函数、一个文件系统事实层。**

### 两平面对称(拟写入 architecture.md 作为新铁律)

```
确定性平面:  flow.LoadState   → flow.Route     → Instruction   (穷举规格测试)
语义平面:    arbiter.Collect* → arbiter.Decide* → XxxDecision   (决策落盘 + eval 回归)
              └── 事实采集(IO) ──┘└── 决策核心(可离线重放) ──┘└── Engine 执行 ──┘
```

## 三、Arbiter 场景(最终集,保持最小)

| 场景 | 触发 | 备注 |
|------|------|------|
| `plan_start` | 新书启动 | 选 short/long 规划师 + 扩充过短需求 |
| `intervention` | 用户干预 | 查询 / 长效规则 / 剧情结构调整 / 已写返工 / 完本后返工或拒绝 |
| `worker_failure` | Worker 报错**且确定性分类无出路** | 网络/参数/前置工件缺失等由确定性代码先分类,不送 Arbiter |
| `deadlock` | 上一轮后仍产生同一路由指令 | 计数与终止语义见 §八 必答题 5 |
| `completion_dispute` | **候补,有证据再加** | 卷末完结判定已由 Route 派 architect(分支 10)承担;仅"结构未到边界但故事该收"的中途分歧才需要,真实发生率未知,不预建 |

完本总结不是裁定,是生成任务——由 Engine 直接派 editor 或一次普通 LLM 调用完成,不占 Arbiter 场景。

## 四、Arbiter 设计

### 4.1 逐场景 Decision 类型(v3 修订:放弃万能结构)

```go
// 共享子类型,防各场景漂移
type DispatchDecision struct {
    Instruction flow.Instruction
    Expect      DispatchExpect // 见 §五
}

type PlanStartDecision struct {
    Planner string // architect_long | architect_short
    Task    string // 含扩充后的需求
    Reason  string
}

type InterventionDecision struct {
    Answer   string
    Rules    string
    Hold     *AdvanceHoldOp
    Reopen   *ReopenOp
    Dispatch *DispatchDecision
    Reason   string
}

type FailureDecision struct {
    Action   string // retry | reroute | abort
    Dispatch *DispatchDecision
    Reason   string
}
```

演进记录:动作列表(顺序校验多余、多态数组易错)→ 万能扁平结构(顺序非法不可表达,但组合非法要靠场景×动作矩阵校验)→ **逐场景类型(场景不匹配的动作不可表达,矩阵校验消失,单场景 schema 更小、LLM 输出更稳、eval 可按场景分评)**。Validate 收缩为逐类型的事实校验(phase 约束等)。

### 4.2 API:每场景一对显式函数

```go
func CollectInterventionFacts(st *store.Store) InterventionFacts        // IO 边界,同 flow.LoadState 纪律
func DecideIntervention(ctx, model, facts, text) (InterventionDecision, error) // 除一次 LLM 调用外无 IO,可离线重放
// 其余场景同形一对;Collect/Decide 形状统一,不建通用 Question/Decision 框架
```

- **失败路径**:JSON 解析或校验失败带错误重问(最多 3 次);仍失败或模型调用失败——干预显式回显真实错误且不产生写入,启动显式报错,failure/deadlock 按确定性兜底(终止并告警)
- **干预记忆**:decisions.jsonl 兼任干预历史,`CollectInterventionFacts` 纳入最近 N 条裁定摘要
- **模型**:Arbiter 统一使用 Default，不暴露独立 role；只在出现明确的能力或成本需求时再扩展配置契约

### 4.3 审计(小而稳定;审计≠恢复源)

```json
{"schema_version":1,"id":"...","kind":"intervention","checkpoint_seq":123,
 "input":"...","facts":{...},"decision":{...},"reason":"...","duration_ms":1200}
```

(token/成本不在记录内——裁定模型经 usageTrackedModel 包装,用量统一进 UsageTracker/预算,与各 Worker 同一套账。)

- facts 只存结构化事实 + 摘要 + artifact/checkpoint 引用,**不复制正文、不存完整上下文包**;单条大小上限,超限截断并标记
- **input 保留在记录内**(离线重放 `Decide*(facts, input)` 必需——没有 input 的审计无法回归);脱敏发生在 **diag export 边界**,不发生在落盘时
- 审计日志不是事件溯源,也不是恢复数据源

## 五、状态提交协议(串行 Engine 循环)

```
读事实 → Route / Arbiter 产出决定 → 核对前置条件 → 执行动作
       → Worker 运行 → 重算 Route 后置条件 → 下一轮
```

- **不变量:控制状态只在 Engine 边界串行变更。**干预可在 Worker 运行期间并行咨询(只读安全、用户秒级看到 Answer/Reason 回显),但**改控制态的动作(hold/reopen/dispatch)进 Engine 队列,边界核对后提交**;answer(无状态)与 rules(内容平面,本章旧规则下章生效即语义)即时执行
- 每个 Dispatch 携带 Collect 时刻快照,边界对账,不符 → 丢弃、记 `decision_stale`、以新事实重询:

```go
type DispatchExpect struct {
    CheckpointSeq int64
    Phase         domain.Phase
    Flow          domain.FlowState
    QueueHead     int
}
```

- 明确前置条件优于全局 Store 哈希(可读、可诊断);不做全局 digest

## 六、恢复模型(只恢复事实,不恢复会话)

```
启动 → 读 Progress → 读最新 Checkpoint → 查 PendingSteer/AdvanceHold/章节许可 → Gate 对账 → Route → 继续运行 Worker
```

plan_start 的恢复依赖单一持久化事实(RunMeta 内),**裁定先落事实、再起执行**:

```go
type PlanStartRecord struct {
    RawPrompt   string
    Planner     string
    PlannerTask string
    DecisionID  string // 关联审计记录
    Status      string // decided | dispatched | done —— 启动事务中间态显式化
}
```

崩溃于任意点:Record 存在则按 Status 续走,不重复咨询;Record 缺失视同新书重询(重询可接受,审计留双记录)。

## 七、迁移路线(v3 重排:Engine 先行,Arbiter 后接线)

顺序调整的依据:原"Arbiter 先行"需要一套过渡管线(裁定经 steering 伪装成 Host 指令喂给 Coordinator);**Engine 先落地则该管线整个不用建**,Arbiter 直接对 Engine 执行器接线。每一步都在删东西,不建临时桥;"双脑期权"顾虑被顺序结构性消解。

| # | 步骤 | 状态 |
|---|------|------|
| 0 | 无条件项:规划补齐入 Router(穷举规格先行);decisions.jsonl 审计。实现改进:规划师身份从既有 `RunMeta.PlanningTier` 推导,无需新增记录机制 | ✅ 2026-07-12 |
| 1 | 文风层交付(docs/voice-layer.md) | ✅ 2026-07-12 |
| 2 | Step 2 RFC 定稿(docs/engine-rfc.md,七道必答题) | ✅ 2026-07-12 |
| 3 | WorkerRunner:确认 subagent.Tool 可程序化直调,事件经 ctx ToolProgress 中继 | ✅ 2026-07-12 |
| 4-5 | Engine 接管全部派发 + Arbiter 四场景接线(plan_start/intervention/failure/deadlock),直连 Engine 执行器(实施中发现 Engine 先行使 steering 过渡管线整个不用建,4/5 合并落地) | ✅ 2026-07-12 |
| 6 | 删除 Coordinator 及全部配套(§十清单全部执行);端到端集成测试(真实工具写完整书/失败裁定/僵局裁定) | ✅ 2026-07-12 |

## 八、Step 2 RFC 必答题(未定稿不进入步骤 3)

1. **Worker 提取面**:WorkerRunner API;build.go 装配件全量的所有权与生命周期——角色模型/failover、prompt cache key、ThinkingLevel、UsageRecorder、SessionLogger、Writer ContextManagerFactory、RestorePack、StopGuardFactory、StopAfterTools、Observer 嵌套事件投影
2. **Engine 生命周期**:启动/暂停/中止/恢复;单 Worker 串行保证;/model 与 thinking 运行时切换
3. **状态提交协议完整化**:§五 的 Expect 对账全场景化;Gate 拆除后 Engine 前置条件清单
4. **错误分类学**:确定性分类(retry/reroute/terminal)先行,仅无出路者送 `worker_failure`;与 agentcore 层重试的分层
5. **僵局协议**:同一 `Agent+Task` 连续重现即说明路由后置条件未满足；Worker 内部中间 checkpoint 不清零；Arbiter 决定 retry 不清零；3 次咨询、5 次硬熔断。
6. **崩溃语义**:如何判定上一个 Worker 是否已产生有效事实
7. **原型验收**:Observer/Usage/Context/模型切换/恢复五项与现状逐位对照

## 九、价值账目

| 维度 | 现状 | 终态 |
|------|------|------|
| 每章 LLM 开销 | 每边界一次转发调用 | 省去;转发失败问题类消失 |
| 裁定可测性 | ~零(混在长会话) | 逐场景离线重放 + eval 回归 |
| 干预响应 | 等章节边界(分钟级) | 咨询即时,控制态边界提交 |
| 复杂度 | 七项补丁生态 | 净减 1500+ 行,三个问题类退役 |
| 崩溃恢复 | 会话重放 + 恢复协议 | 读 store 续跑 |
| 过渡期风险 | — | 集中在步骤 3/4(Worker 提取),由 RFC + 原型关卡控制;步骤 0/1 无条件成立 |

## 十、终态删除清单

Coordinator 及其会话恢复逻辑、Coordinator StopGuard、Dispatcher steering 协议与 `[Host 下达指令]` 文本协议、FlowBoundaryHook、completePhaseGate / writerExpandedChapterGate(校验平移为 Engine 前置条件)、MaxTurns=100_000、coordinator.md 全部行为规训。

## 十一、异议与评审记录

1. *"裁定正确性不会提升"*——成立;真实差异是聚焦/策展/前置校验 vs 会话记忆,净值略优且首次可测量
2. *"现状跑通,动控制面冒险"*——承认;地基为此而建,分步可停可退
3. *"架构不是瓶颈,内容质量才是"*——部分成立,文风层先行
4. **评审一(2026-07-12)**:状态提交协议缺失→§五;Step 2 单薄→§八必答题+原型关卡;启动时序→§六;"非法状态不可表达"过度声明→4.1 演进记录;arbiter 角色白名单事实错误→4.2;审计卫生→4.3
5. **评审二(2026-07-12)**:逐场景 Decision 类型(采纳,4.1);迁移顺序重排 Engine 先行(采纳,§七);统一边界提交(采纳,§五);PlanStartRecord(采纳,§六);不重命名 host(采纳);字数建议留协议文件(采纳,见 voice-layer)。**保留意见**:审计必须保留 input 否则无法重放(4.3);completion_dispute 降为候补场景(§三)

## 十二、纪律与不做

**纪律**:①新决策点先过 §二 三分法,禁止默认"给 prompt 加规则";②每个 LLM 决策点必备事实清单/结构化输出/降级路径/落盘审计;③只写事实护栏不写行为护栏;④声明式不变量优于程序性脚本;⑤控制面改动先改穷举规格再改实现。

**不做**:事件溯源重写;为假想多租户抽象 Store;通用工作流 DSL;全局 State Digest;host 包重命名;completion_dispute 预建。
