# 提示词缓存设计：litellm / agentcore / ainovel 三层协同

> 本文是一份讲解材料：介绍我们如何在三个协作仓库中设计端到端的 LLM 提示词缓存
> （prompt caching），包含设计原理、真实排查案例与可对照的源码位置。
>
> - **litellm** —— LLM 网关：协议翻译与能力声明
> - **agentcore** —— Agent 框架：缓存放置与缓存身份
> - **ainovel-cli** —— 应用层：一行配置接入（codebot 同理）

---

## 1. 为什么值得做：成本模型与一个真实案例

Agent 系统的请求有个结构特点：**每一轮请求都携带全部历史**。一个 30 轮的工具循环，
第 30 轮的请求体里包含前 29 轮的所有消息。不做缓存时，同样的前缀字节被反复计费。

两大厂商的缓存定价（以 Anthropic 为例）：

| 项目 | 相对普通输入价 |
|---|---|
| 缓存写入（5 分钟 TTL） | 1.25x |
| 缓存写入（1 小时 TTL） | 2x |
| **缓存读取** | **0.1x（省 90%）** |

真实案例：一次 33 章的长篇小说生成跑掉了 $58，事后分析 `meta/usage.json` 发现
**整体缓存命中率只有 8.5%**（coordinator 仅 2.7%，architect 为 0）。逐请求比对
usage 序列（input vs cache_read）后定位出三个根因：

1. **tools 字节抖动**：subagent 工具的 Description/Schema 每轮从 Go map 直接迭代重建，
   顺序随机 → 请求体从第 0 字节起就与上一轮不同 → 前缀缓存全部失效；
2. **没有路由亲和**：OpenAI 系没传 `prompt_cache_key`，字节完全相同的请求也可能被
   负载均衡到没有缓存的实例（铁证：33 个会话中字节相同的首请求只命中 12 个）；
3. **Claude 系零断点**：Anthropic 是显式缓存，不打 `cache_control` 断点 = 完全没有缓存。

这三个根因分别对应下文的三块设计：**前缀稳定纪律**、**缓存身份**、**断点编排**。

---

## 2. 预备知识：两种缓存协议的心智模型

### 2.1 OpenAI：自动前缀缓存（隐式）

- 服务端自动对 **≥1024 tokens** 的前缀做缓存，无需客户端声明；
- 命中按 128-token 对齐的粒度增长；
- 请求可带 `prompt_cache_key`（官方字段）做**路由亲和**——同 key 的请求尽量落到
  同一缓存分片；
- usage 里 `cached_tokens` 报告命中量；**缓存写入永远不上报**（`cache_write` 恒 0
  是正常现象，不是 bug）。

### 2.2 Anthropic：显式断点（cache_control）

- 客户端在内容块上打 `cache_control` 断点，**断点覆盖它之前的一切**
  （顺序固定为 tools → system → messages）；
- 每请求**最多 4 个断点**；
- 写价 1.25x（5m）/ 2x（1h），读价 0.1x；
- `cache_control` **不允许打在 thinking 块上**（会被 400 拒绝）。

### 2.3 共同前提

无论隐式还是显式，缓存都只认**字节级前缀相等**。所以一切设计的地基是同一句话：

> **把变化频率从低到高排序整个请求：静态的放最前面，动态的放最后面，
> 且已发送的历史一个字节都不能变。**

---

## 3. 总体架构：三层分工

```
┌────────────────────────────────────────────────────────┐
│ 应用层（ainovel-cli / codebot）                          │
│   决定"缓存身份"取什么值：一书一基、一角色一名             │
│   接入成本 = 每个 agent 两行配置                          │
├────────────────────────────────────────────────────────┤
│ agentcore（Agent 框架）                                  │
│   决定"断点放哪、key 何时派生"：                          │
│   system 地板 + 末消息滚动尖端；spawn 追加 #seq；          │
│   按 provider 能力门控，不支持则静默丢弃                   │
├────────────────────────────────────────────────────────┤
│ litellm（LLM 网关）                                      │
│   纯协议翻译：cache_control ↔ 各厂商字段、                │
│   prompt_cache_key 透传、Capabilities 能力声明            │
│   不做任何"要不要缓存"的决策                              │
└────────────────────────────────────────────────────────┘
```

切分原则：**litellm 只回答"这个端点支持什么"，agentcore 只回答"缓存点放在哪"，
应用层只回答"身份是什么"**。每层单独可测，应用换一个（codebot 复用同一套
agentcore/litellm）不用重写缓存逻辑。

---

## 4. 根基：前缀字节稳定三纪律

缓存收益的前提是前缀字节稳定。三条纪律各自对应一次真实事故。

### 纪律一：tools 序列化必须字节确定

事故：`subagent` 工具把注册的 agent 列表嵌进自己的 Description/Schema，而列表来自
Go map 迭代——每次调用顺序随机，tools 字节每轮都变，coordinator 命中率因此只有 2.7%。
（Claude Code 团队也被同款问题咬过：他们的全 fleet 曾因此多付 10.2% 的缓存写入。）

修复（agentcore `subagent/subagent.go`）：

```go
// sortedAgentNames returns registered agent names in deterministic order.
// Description and Schema are rebuilt on every LLM call; iterating the map
// directly would shuffle their bytes across requests and defeat provider
// prefix caching (tools serialize into the cached prompt prefix).
func (t *Tool) sortedAgentNames() []string {
	return slices.Sorted(maps.Keys(t.agents))
}
```

> 教训的一般形式：**任何进入请求体的集合，序列化前必须排序**。Go 的 map 迭代
> 随机化会把这个 bug 藏得很深——功能完全正常，只有账单不正常。

### 纪律二：历史必须 append-only（压缩要"提交"）

事故：writer 的上下文压缩策略是"投影"（每次调用时临时改写历史视图，但不落回
基线）。一旦超过阈值，**每一轮都在重新改写整个前缀** → 每轮全 miss。

修复：投影后提交（`CommitOnProject: true`），让改写只发生一次，之后恢复
append-only，直到下次越过阈值。

> 一般形式：上下文压缩是**计划内的一次性断裂**（重置前缀，付一次全价），
> 这没问题；不能接受的是**每轮都断**。压缩要么不做，要么做完固化。

### 纪律三：动态内容进尾部

每轮变化的东西（世界状态信封、每轮提醒、最新工具结果）只允许**追加在消息尾部**，
绝不回头修改中段。ainovel 的 `novel_context` 信封就是尾部追加式设计——它每章都变，
但它变不影响前面几十万 token 的缓存。

---

## 5. 缓存身份：一书一基、一角色一名、一会话一键

OpenAI 系的 `prompt_cache_key` 解决的是**路由问题**：字节相同的请求若被负载均衡到
不同实例，照样 miss。key 的设计目标是"同一条缓存血统的请求，永远带同一个 key"。

我们的三级身份（ainovel `internal/agents/build.go`）：

```go
// promptCacheBase 从书目录派生稳定短哈希，作为提示词缓存身份前缀：同一本书
// 跨进程重启共享路由桶，且不向 provider 泄露本地路径。角色后缀由调用方拼接，
// subagent 每次 spawn 再追加 "#seq"（一次会话一个键）。
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}
```

应用层接入就是每个 agent 两行：

```go
writer := subagent.Config{
	// ...
	CacheLastMessage: "ephemeral",                // Claude 断点开关（见 §6）
	PromptCacheKey:   cacheBase + "-writer",      // OpenAI 路由身份（角色级）
}
// coordinator（顶层 Agent）同理：
agentcore.WithCacheLastMessage("ephemeral"),
agentcore.WithPromptCacheKey(cacheBase+"-coordinator"),
```

第三级（会话级）由 agentcore 自动派生——每次 spawn 一个新会话，就是一条新的
缓存血统（agentcore `subagent/subagent.go`）：

```go
runSeq := t.runSeq.Add(1)

// One conversation, one cache key: suffix the per-run sequence so each
// spawn forms its own cache lineage instead of piling every run of this
// agent into a single routing bucket.
promptCacheKey := cfg.PromptCacheKey
if promptCacheKey != "" {
	promptCacheKey = fmt.Sprintf("%s#%d", promptCacheKey, runSeq)
}
```

最终形态：`nvl-a1b2c3-writer#17` = 这本书、writer 角色、第 17 次 spawn 的会话。

> 为什么不是全局一个 key？不同会话前缀不同，混在一个路由桶里会稀释命中。
> 为什么不带时间戳/随机数？key 必须**跨请求稳定**，会话内每轮都要相同。

codebot 的对应设计：key 语义 = SessionID（切会话 = 换血统），teammate 追加名字
后缀，宿主复用同一 Agent 实例切会话时调 `Agent.SetPromptCacheKey` 重指身份。

---

## 6. Claude 断点编排：地板 + 滚动尖端

Anthropic 不打断点 = 零缓存。我们的预算分配（上限 4 个断点/请求）：

```
[tools][system ←断点①"地板"][...历史消息...][最新消息 ←断点②"滚动尖端"]
```

### 6.1 地板（floor）：钉住静态前缀

system prompt 是最大的静态块。给它一个专属断点，保证**新会话/尾部缓存被逐出时，
至少 system+tools 前缀仍然从缓存读**（agentcore `loop.go`）：

```go
} else if agentCtx.SystemPrompt != "" {
	m := SystemMsg(agentCtx.SystemPrompt)
	if config.CacheLastMessage != "" {
		// Cache floor: pin the static system prompt with its own
		// breakpoint so a fresh session — or a turn whose tail entry was
		// evicted — still reads the system+tools prefix from cache.
		m.Metadata = map[string]any{"cache_control": config.CacheLastMessage}
	}
	prefix = append(prefix, m)
}
```

### 6.2 滚动尖端（rolling tip）：每轮推进覆盖面

把一个断点打在**最后一条非 system 消息**上。工具循环里每次 LLM 调用都会写一条
覆盖到最新 tool_use+tool_result 的缓存，下一轮直接读，不再重传：

```go
// markLastMessageForCache returns a copy of messages with cache_control attached
// to the metadata of the last non-system message. System messages are skipped so
// trailing per-turn reminders (which change every turn) don't end up carrying
// the breakpoint.
func markLastMessageForCache(messages []Message, cacheControl string) []Message {
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != RoleSystem {
			idx = i
			break
		}
	}
	// ...
}
```

注意跳过尾部 system reminder：它每轮都变，把断点打在它身上等于每轮写一条
永远不会被复用的缓存。

### 6.3 末块语义：一条消息只烧一个断点

消息级的 `cache_control` 语义是"在这条消息之后写一个断点"。翻译到块级时只允许
落在**最后一个可缓存块**上——给每个块都打标会把 4 个断点的预算烧穿；而 Anthropic
拒绝 thinking 块携带 `cache_control`，所以从尾部扫、跳过 reasoning
（agentcore `llm/litellm.go`）：

```go
if cache != nil {
	// Anthropic rejects cache_control on thinking blocks — land the
	// breakpoint on the last cacheable block instead.
	for i := len(blocks) - 1; i >= 0; i-- {
		if _, isReasoning := blocks[i].(litellm.ReasoningBlock); isReasoning {
			continue
		}
		blocks[i] = withBlockCache(blocks[i], cache)
		break
	}
}
```

### 6.4 TTL 管道

配置值约定为 `"type[:ttl]"` 字符串，如 `"ephemeral"`（默认 5m）或 `"ephemeral:1h"`：

```go
func cacheControlFromMetadata(metadata map[string]any) *litellm.CacheControl {
	value, _ := metadata["cache_control"].(string)
	if value == "" {
		return nil
	}
	if typ, ttl, ok := strings.Cut(value, ":"); ok {
		return &litellm.CacheControl{Type: typ, TTL: ttl}
	}
	return &litellm.CacheControl{Type: value}
}
```

要不要升 1h 用数据说话：写价从 1.25x 涨到 2x，只有实测调用间隔经常超过 5 分钟
才值得（我们实测 coordinator 中位间隔 172s，没升）。

---

## 7. 安全发送：能力门控 + 官方端点判定

### 7.1 能力门控：不支持的字段不出门

litellm 各 provider 对 `ProviderOptions` **严格校验**（未知键直接报错），所以
agentcore 在发送前按能力声明门控（agentcore `llm/litellm.go`）：

```go
// Prompt-cache routing identity. Capability-gated: litellm providers
// validate provider options strictly, so an unsupported key must be
// dropped here rather than rejected there.
if callCfg.PromptCacheKey != "" && caps.Cache.PromptKey == litellm.SupportYes {
	req.ProviderOptions["prompt_cache_key"] = callCfg.PromptCacheKey
}
```

### 7.2 官方端点判定：兼容生态没有未知字段契约

`prompt_cache_key` 是 OpenAI 官方字段，但"OpenAI 兼容"端点的行为没有任何统一契约。
联网实证（2026-07）：

- **严格端直接拒绝**：Groq、Cerebras、火山引擎、Fireworks 对该字段返回 400/422
  （Zed #36215、OpenClaw #48155 都因此改成条件发送）；
- **重编组型中转静默丢弃**：one-api/new-api/sub2api 的非透传路径把请求体解析进
  结构体再 re-marshal，未知字段无声消失（发了白发）；
- **宽松端忽略**：Ollama、当前版 vLLM、MiniMax。

所以 litellm openai provider 的能力声明按 BaseURL **动态**判定
（litellm `provider/openai/capabilities.go`）：

```go
// promptCacheParamsSupport reports whether this endpoint is trusted to accept
// OpenAI's prompt cache params (prompt_cache_key / prompt_cache_retention).
// Only the official endpoint guarantees the field contract.
func (p *Provider) promptCacheParamsSupport() litellm.Support {
	if p.cfg.PromptCacheParams || isOfficialBaseURL(p.cfg.BaseURL) {
		return litellm.SupportYes
	}
	return litellm.SupportUnknown
}

func isOfficialBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}
```

官方 `api.openai.com` → `SupportYes`（发送）；第三方 BaseURL → `SupportUnknown`
（§7.1 的门控自动不发，**默认永不炸任何端点**）；确认自己的中转原样透传的用户，
在 provider 配置里显式 opt-in：

```jsonc
"my-relay": {
  "type": "openai",
  "base_url": "https://relay.example.com/v1",
  "extra": { "prompt_cache_params": true }   // 我确认这个中转透传请求体
}
```

> 为什么开关做在 litellm 能力层而不是应用配置层？因为运行时 `/model` 切 provider
> 会换 client，能力声明跟着 client 自动切换；应用构造期的判定覆盖不了运行时切换。

---

## 8. 观测：缓存链断裂检测

缓存是"看不见的功能"——坏了不报错，只是变贵。所以要有观测（借鉴 Claude Code 的
promptCacheBreakDetection，做了轻量版）。

判定口径（ainovel `internal/host/usage.go`）：

```go
// 同一会话（role+task）内：前缀未缩短，而命中量较上次下降 >5% 且降幅 ≥2000 tokens
broke := prevPrefix > 0 && prefix >= prevPrefix &&
	float64(u.CacheRead) < float64(prevRead)*cacheBreakKeepRatio &&
	prevRead-u.CacheRead >= cacheBreakMinDropTokens
```

四个关键设计，每个都对应一类误报：

| 设计 | 防的误报 |
|---|---|
| **双阈值**（相对 5% 且绝对 2000） | 单一相对阈值被小前缀噪声淹没；单一绝对阈值漏掉大前缀退化 |
| **基线跟随会话（role+task）** | 检测维度必须与 `prompt_cache_key` 的会话粒度（`#seq`）对齐；按 role 跨会话比较，会在"上一会话很短、新会话首请求前缀反而更长"时误报（Codex review 抓到的真实缺口） |
| **前缀缩短 = 合法重置** | 上下文压缩是计划内断裂，重置基线不告警 |
| **replay 不检测** | 启动时重放历史会把陈年断裂刷成新告警 |

告警时按时间间隔给归因提示：间隔 >1h → 疑似 1h TTL 过期；>5m → 疑似 5m TTL 过期；
很短 → 疑似服务端逐出/路由漂移（**中转站轮询上游账号是最常见原因**）。计数持久化到
`usage.json` 并显示在 TUI 缓存面板的"链路断裂"行。

---

## 9. 闩锁红线：会话单调原则

一条对未来功能的宪法级约束：

> **一切会进入缓存前缀的量（system prompt、tools、thinking 参数、采样参数），
> 在会话内首次计算后必须冻结——宁可陈旧，不可破缓存。**

例子:"运行时调 thinking 强度"这类功能，如果让新强度立即作用于进行中的会话，
等于每次调整都重写前缀、作废全部缓存。正确做法是新值只对**新 spawn 的会话**生效。
任何"运行时可调 X"的需求评审，第一个问题都是：X 在不在缓存前缀里？

---

## 10. 常见误判与天花板

1. **OpenAI 的 `cache_write` 恒 0 是正常的**——API 不上报写入量，别当 bug 查。
2. **中转站天花板**：中转若轮询多个上游账号，客户端字节再稳也会 miss（上游账号 A
   的缓存对账号 B 不可见）。这解释了"字节完全相同的请求只命中 12/33"的谜团。
   **这不是客户端可解的问题**——Claude Code 团队的数据也显示约九成"客户端未变化
   却断裂"的案例是服务端原因。
3. **验证口径**：会话 JSONL 不含 system prompt 和完整请求体，**逐请求 usage 序列
   （input vs cache_read）才是诊断金标准**。一个实用指纹：命中量若恰好钉在
   "system 提示词 token 数按 128 向下取整"，说明只有 system 段命中、消息段全 miss。
4. **收益核算**：读价 0.1x、写价 1.25x，意味着一条缓存只要被读 1 次就回本。
   多轮 agent 会话里断点几乎总是正收益，所以 `CacheLastMessage` 不设开关、默认开。
   **例外（Responses 端点）**：litellm v1.8.11 的 Responses 适配器只允许断点落在
   `input_text` 上；agentcore 的滚动尖端会贴到最后一条非 system 消息，工具循环里
   就是 tool_result（`function_call_output`），本地校验直接拒绝、请求发不出去
   （`cache breakpoints are not supported on tool result blocks`）。因此
   `internal/agents/build.go` 的 `explicitCacheControl` 对 `api: "responses"` 的
   provider 关闭 `CacheLastMessage`，只保留 `prompt_cache_key` 路由亲和；Anthropic
   与 chat 端点不受影响。

---

## 11. 接入指南速查

**ainovel-cli**（已内置）：每个 agent 配 `CacheLastMessage: "ephemeral"` +
`PromptCacheKey: promptCacheBase(bookDir) + "-<role>"`，其余全自动。provider 声明
`api: "responses"` 时断点自动关闭（litellm v1.8.11 的 Responses 适配器不接受
tool_result 上的断点，见 §10.4），`prompt_cache_key` 照旧。

**codebot**（已内置）：key = SessionID；`Reset`/`SwitchSession` 时
`agent.SetPromptCacheKey(newSessionID)`；teammate 用 `sessionID + "-" + name`。

**新应用接 agentcore** 的最小清单：

```go
agentcore.NewAgent(
	agentcore.WithCacheLastMessage("ephemeral"),   // Claude 断点：地板+滚动尖端
	agentcore.WithPromptCacheKey(stableIdentity),  // OpenAI 路由：稳定、每会话唯一
	// ...
)
```

外加自查三问（对应三纪律）：

1. 我的 tools 序列化是字节确定的吗？（集合都排序了吗）
2. 我的历史是 append-only 的吗？（压缩会提交吗）
3. 我每轮变化的内容都在尾部吗？

---

## 12. 给学习者的经验清单

- 缓存优化的本质是**字节纪律**，不是调参：先保证前缀稳定，再谈 key 和断点。
- 诊断永远从**逐请求 usage 序列**开始，不要从代码猜。
- Go map 迭代随机化 + 请求体序列化 = 最隐蔽的缓存杀手，功能测试永远发现不了。
- "OpenAI 兼容"是营销词不是契约：官方字段发给第三方端点前，先找一手证据
  （源码/issue/同类客户端的已落地修法），"一般会忽略"是危险的推断。
- 观测要防误报优先：检测维度必须与缓存血统的粒度对齐；宁可漏报不可误报，
  否则告警很快会被无视。
- 分层的检验标准：换一个应用（codebot）接入时，缓存逻辑一行都不用重写。

---

### 附：源码索引

| 主题 | 位置 |
|---|---|
| tools 确定性排序 | agentcore `subagent/subagent.go` `sortedAgentNames` |
| 会话级 key 派生（#seq） | agentcore `subagent/subagent.go` `runAgent` |
| system 地板 + 滚动尖端 | agentcore `loop.go` `callLLM` / `markLastMessageForCache` |
| 末块断点 + 跳 thinking | agentcore `llm/litellm.go` `convertAgentBlocks` |
| TTL 解析（"ephemeral:1h"） | agentcore `llm/litellm.go` `cacheControlFromMetadata` |
| 能力门控 | agentcore `llm/litellm.go` `applyCallConfig` |
| 官方端点判定 + opt-in | litellm `provider/openai/capabilities.go` / `provider.go Config` |
| 缓存身份（一书一基） | ainovel `internal/agents/build.go` `promptCacheBase` |
| 断裂检测 | ainovel `internal/host/usage.go` `noteCacheBreak` |
| 架构定位 | ainovel `docs/architecture.md` §6.6 |
