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
│   接入成本 = 每个 agent 两行配置 + system 块地板断点       │
├────────────────────────────────────────────────────────┤
│ agentcore（Agent 框架）                                  │
│   决定"断点放哪、key 何时发出"：                          │
│   滚动尖端 + 回看断点（Config.Cache）；                    │
│   按端点能力门控，不支持则静默丢弃                        │
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

当时修复在 agentcore 的 subagent 工具里：把注册 agent 名单排序后再生
Description/Schema（`sortedAgentNames`）。v1.8.8 重写后这个工具改为按传入顺序
构建（`subagent.New(tasks, agents...)` 直接遍历切片，不再迭代 map），并且
ainovel 已不使用它——Worker 由 Engine 程序化调用，工具集在 `internal/agents/build.go`
用固定切片装配。教训本身不变：

> 教训的一般形式：**任何进入请求体的集合，序列化前必须排序**。Go 的 map 迭代
> 随机化会把这个 bug 藏得很深——功能完全正常，只有账单不正常。

### 纪律二：历史必须 append-only（压缩要"提交"）

事故：writer 的上下文压缩策略是"投影"（每次调用时临时改写历史视图，但不落回
基线）。一旦超过阈值，**每一轮都在重新改写整个前缀** → 每轮全 miss。

修复：**压缩结果一次性提交**。agentcore v1.8.8 起，`Compactor` 返回的是整段
替换后的 history（`Compaction.Messages`），框架在 `CompactionEnd` 时直接写回，
之后继续 append-only，直到下次越过阈值（`Config.CompactAt` 或 provider 报
context overflow）。

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

### 5.1 基：书目录哈希

```go
// promptCacheBase 从书目录派生稳定短哈希，作为提示词缓存身份前缀：同一本书
// 跨进程重启共享路由桶，且不向 provider 泄露本地路径。角色后缀由调用方拼接。
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}
```

### 5.2 名：角色后缀

`internal/agents/build.go` 给每个 Worker 拼角色后缀，写进模型的请求选项
（`bootstrap.WithPromptCacheKey` → `litellm.Request.ProviderOptions`）：

```go
cfgs := map[string]agentcore.Config{
	"writer": {
		Model:  workerModel(writerModel, cacheBase+"-writer", writerThinking),
		System: workerSystem(writerPrompt, cache),   // 地板断点，见 §6.1
		Cache:  cache,                               // 滚动尖端 + 回看断点，见 §6.2
		// ...
	},
}
```

`prompt_cache_key` 是否真的出门由目标端点决定：只有声明支持该字段、且是官方
`api.openai.com` 或显式开启 `prompt_cache_params` 的中转才会带上（见 §7.2）。
Anthropic 与 Bedrock 不使用路由键，它们的身份由断点位置本身表达。

### 5.3 键：一个 Worker 一条会话

v1.8.8 起 Worker 不再"每次 spawn 一个新会话"：`agents.WorkerSet` 为每个 Worker
保存一条**跨 run 累积的 history**（`agentcore.Run` 无状态，返回结束时的 history，
WorkerSet 负责存回）。因此"一会话一键"退化为"一角色一键"——同一本书里 writer
的所有章节写作共享同一条缓存血统，这恰恰是命中率最高的形态。

最终形态：`nvl-a1b2c3-writer` = 这本书 + writer 角色。角色内的历史只追加，
压缩才是一次计划内的断裂。

> 为什么不是全局一个 key？不同角色的 system prompt 与 tools 不同，前缀本就不同，
> 混在一个路由桶里会稀释命中。
> 为什么不带时间戳/随机数？key 必须**跨请求稳定**，会话内每轮都要相同。

codebot 的对应设计：key 语义 = SessionID（切会话 = 换血统），teammate 追加名字
后缀。

---

## 6. Claude 断点编排：三处断点

Anthropic 不打断点 = 零缓存。我们实际发出的断点有三处（每请求上限 4 个）：

```
[tools][system ←断点①地板][...历史消息...][上一条请求的末块 ←断点③回看][最新消息 ←断点②滚动尖端]
```

### 6.1 地板（floor）：钉住静态前缀

system prompt 是最大的静态块。应用层把它包成带 `Cache` 的块，保证**新会话/尾部
缓存被逐出时，至少 tools+system 前缀仍然从缓存读**（ainovel `internal/agents/build.go`）：

```go
// workerSystem 把提示词包成带缓存断点的 system 块（缓存地板）。
func workerSystem(prompt string, cache *litellm.CacheControl) []litellm.Block {
	return []litellm.Block{litellm.TextBlock{Text: prompt, Cache: cache}}
}
```

地板放在应用层而不是框架里：system prompt 是应用构造的，它是否值得一个专属断点
只有应用知道。

### 6.2 滚动尖端（rolling tip）：每轮推进覆盖面

`agentcore.Config.Cache` 一开，框架在 `BuildCall` 里放两类断点（agentcore
`request.go` `markCache`）：**最后一条消息**（本轮写入，下轮读取），以及**上一条
请求写过的位置**——最新 assistant 响应之前的那条消息。

```go
func markCache(msgs []litellm.Message, cache *litellm.CacheControl) {
	last := len(msgs) - 1
	if last < 0 || msgs[last].Role == litellm.RoleSystem {
		return
	}
	mark(msgs, last, cache)
	for i := last; i > 0; i-- {
		if msgs[i].Role == litellm.RoleAssistant {
			if msgs[i-1].Role != litellm.RoleSystem {
				mark(msgs, i-1, cache)
			}
			return
		}
	}
}
```

为什么需要第三个断点：provider 从断点回看已有写入的距离有限（Anthropic 是 20 个
块），一轮里并行工具调用新增很多块时，如果只有尖端断点，下一轮可能回看不到上一轮
写的位置。补一个"上一次请求末尾"的断点，就把它钉在实际写过的地方。

尾部如果是 system reminder（每轮都变），框架直接不打断点——打在它身上等于每轮写
一条永远不会被复用的缓存。

### 6.3 末块语义：一条消息只烧一个断点

消息级的断点语义是"在这条消息之后写一个断点"。落到块级时只允许打在**最后一个可
缓存块**上——给每个块都打标会把 4 个断点的预算烧穿；而 Anthropic 拒绝 thinking
块携带 `cache_control`，所以从尾部扫、跳过 reasoning 块（agentcore `request.go`
`withCache`）：

```go
func withCache(block litellm.Block, cache *litellm.CacheControl) (litellm.Block, bool) {
	c := *cache
	switch b := block.(type) {
	case litellm.TextBlock:
		b.Cache = &c
		return b, true
	case litellm.ToolUseBlock:
		b.Cache = &c
		return b, true
	case litellm.ToolResultBlock:
		b.Cache = &c
		return b, true
	// ...
	}
	return block, false
}
```

`ReasoningBlock` 不在这个 switch 里：它拿不到断点，扫描会继续向前找可缓存的块。
tools 段由 provider 自己拼接在前缀最前面，不需要额外断点。

### 6.4 TTL

`litellm.CacheControl` 只有一个 `TTL` 字段，空值 = 交给 provider 默认
（Anthropic / Bedrock 是 5 分钟）。ainovel 显式留空：

```go
// workerCacheTTL 是显式缓存断点的 TTL；空 = 交给 provider 默认。litellm 把 TTL
// 原样透传（anthropic 适配器拼进 cache_control.ttl），而 OpenAI 的
// prompt_cache_breakpoint 没有 ttl 字段，因此这里不写死 "5m"。
const workerCacheTTL = ""

func workerCache() *litellm.CacheControl {
	return &litellm.CacheControl{TTL: workerCacheTTL}
}
```

要不要升 1h 用数据说话：写价从 1.25x 涨到 2x，只有实测调用间隔经常超过 5 分钟
才值得（我们实测中位间隔 172s，没升）。

---

## 7. 安全发送：能力门控 + 官方端点判定

### 7.1 能力门控：不支持的字段不出门

litellm 各 provider 对 `ProviderOptions` **严格校验**（未知键直接报错），所以
发送前必须只保留端点认识的键。agentcore v1.8.8 不再替应用做这件事：请求由调用方
构造，`prompt_cache_key` 通过 `Request.ProviderOptions` 传入，门控落在 ainovel 的
`internal/bootstrap/models.go`：

```go
// mergeOptions 保留调用方带的、目标端点认识的键（如 prompt_cache_key），
// 丢弃它不认识的键（严格校验的端点会直接拒绝），再叠加 extra_body 基线。
func (e *endpoint) mergeOptions(caller litellm.ProviderOptions) litellm.ProviderOptions {
	merged := make(litellm.ProviderOptions, len(caller)+len(e.options))
	for key, value := range caller {
		if _, owned := e.options[key]; owned {
			continue
		}
		if e.acceptsOption(key) {
			merged[key] = value
		}
	}
	for key, value := range e.options {
		merged[key] = value
	}
	return merged
}

func (e *endpoint) acceptsOption(key string) bool {
	if key == openai.ProviderOptionPromptCacheKey {
		return e.promptCacheKey
	}
	return slices.Contains(e.caps.ProviderOptions, key)
}
```

`e.caps` 来自 litellm 的 `Capabilities()`；v1.9.6 的 OpenAI provider 把
`prompt_cache_key` 列进 chat 与 responses 两侧的可用选项，因此剩下唯一的判定是
"这个端点是否值得信任"（§7.2）。

### 7.2 官方端点判定：兼容生态没有未知字段契约

`prompt_cache_key` 是 OpenAI 官方字段，但"OpenAI 兼容"端点的行为没有任何统一契约。
联网实证（2026-07）：

- **严格端直接拒绝**：Groq、Cerebras、火山引擎、Fireworks 对该字段返回 400/422
  （Zed #36215、OpenClaw #48155 都因此改成条件发送）；
- **重编组型中转静默丢弃**：one-api/new-api/sub2api 的非透传路径把请求体解析进
  结构体再 re-marshal，未知字段无声消失（发了白发）；
- **宽松端忽略**：Ollama、当前版 vLLM、MiniMax。

所以"是否发路由键"由 BaseURL **动态**判定（ainovel `internal/bootstrap/models.go`；
litellm v1.9.6 的 openai provider 允许兼容端透传未列出的选项，判定权因此回到应用层）：

```go
// promptCacheKeyAllowed 复刻旧适配器的门控：只有确认透传 prompt_cache_key 的
// OpenAI 端点才发路由键——官方端点，或 prompt_cache_params 显式开启的代理。
func promptCacheKeyAllowed(typeName, baseURL string, caps litellm.Capabilities, capsOK, optIn bool) bool {
	if !capsOK || typeName != "openai" {
		return false
	}
	if !slices.Contains(caps.ProviderOptions, openai.ProviderOptionPromptCacheKey) {
		return false
	}
	return optIn || isOfficialOpenAI(baseURL)
}
```

官方 `api.openai.com` → 发送；第三方 BaseURL → 不发（§7.1 的门控自动丢弃，
**默认永不炸任何端点**）；确认自己的中转原样透传的用户，在 provider 配置里显式
opt-in：

```jsonc
"my-relay": {
  "type": "openai",
  "base_url": "https://relay.example.com/v1",
  "extra": { "prompt_cache_params": true }   // 我确认这个中转透传请求体
}
```

> 为什么按端点而不是按模型判定？同一个 provider 键下多个模型共享一个 client，
> 可信度是端点属性；运行时 `/model` 切换可能换端点，判定必须跟着端点走。

---

## 8. 观测：缓存链断裂检测

缓存是"看不见的功能"——坏了不报错，只是变贵。所以要有观测（借鉴 Claude Code 的
promptCacheBreakDetection，做了轻量版）。

判定口径（ainovel `internal/host/usage.go`）：

```go
// 同一会话（role+task）内：前缀未缩短，而命中量较上次下降 >5% 且降幅 ≥2000 tokens
prefix := u.InputTokens // litellm.Usage.InputTokens 含缓存读写
broke := prevPrefix > 0 && prefix >= prevPrefix &&
	float64(u.CacheReadTokens) < float64(prevRead)*cacheBreakKeepRatio &&
	prevRead-u.CacheReadTokens >= cacheBreakMinDropTokens
```

四个关键设计，每个都对应一类误报：

| 设计 | 防的误报 |
|---|---|
| **双阈值**（相对 5% 且绝对 2000） | 单一相对阈值被小前缀噪声淹没；单一绝对阈值漏掉大前缀退化 |
| **基线按 role+task** | 检测维度必须与缓存血统对齐；按 role 跨任务比较，会在"上一任务很短、新任务首请求前缀反而更长"时误报（Codex review 抓到的真实缺口） |
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

例:"运行时调 thinking 强度"这类功能——它只改模型请求参数（`WorkerSet.SetThinking`
下一次 `Run` 生效），不碰 system、不碰 tools、不重写 history，因此不破缓存，
可以放心做成运行时开关。反过来，任何会改动 system prompt 文本、tools 集合/顺序、
或早期消息的"运行时能力"，都必须等下一次会话（本项目里是下一次 `Run` 的新历史，
或先做一次计划内压缩）再落地。任何"运行时可调 X"的需求评审，第一个问题都是：
X 在不在缓存前缀里？

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
   多轮 agent 会话里断点几乎总是正收益，所以 `Config.Cache` 不设开关、默认开。
5. **历史：#142 的 Responses 例外已经消失**。litellm v1.8.11 的 Responses 适配器
   只允许断点落在 `input_text` 上，工具循环里断点会被贴到 tool_result
   （`function_call_output`）并被本地校验拒绝
   （`cache breakpoints are not supported on tool result blocks`）；当时的修法是
   ainovel 侧对 `api: "responses"` 的 provider 关闭 `CacheLastMessage`、只保留
   `prompt_cache_key`（PR #142）。**litellm ≥ v1.9.2 已支持在 tool result 块上落
   断点，本项目升级到 v1.9.6 后该 gate 已删除**：`workerCache()` 对所有 provider
   统一返回断点配置，Anthropic / chat / Responses 三条路径不再分叉。历史记录保留
   在此，用来说明"临时 gate 必须随上游能力升级一起拆除"，而不是长期存在的行为。

---

## 11. 接入指南速查

**ainovel-cli**（已内置）：每个 Worker 一套 `Config`——
`Cache: workerCache()`（滚动尖端 + 回看断点）、system 块带同一个 `CacheControl`
（地板）、`WithPromptCacheKey(promptCacheBase(bookDir) + "-<role>")`（路由身份）。
所有 provider 一致，没有按 `api` 分叉的开关。

**codebot**（已内置）：key = SessionID；`Reset`/`SwitchSession` 时换 key；
teammate 用 `sessionID + "-" + name`。

**新应用接 agentcore** 的最小清单（v1.8.8 API）：

```go
cfg := agentcore.Config{
	System: []litellm.Block{litellm.TextBlock{Text: systemPrompt, Cache: cache}},
	Cache:  &litellm.CacheControl{},           // 滚动尖端 + 回看断点；TTL 空 = provider 默认
	Model:  withPromptCacheKey(model, stableIdentity), // OpenAI 路由身份
	// ...
}
history, err := agentcore.Run(ctx, cfg, history, agentcore.UserText(task))
```

外加自查三问（对应三纪律）：

1. 我的 tools 序列化是字节确定的吗？（集合都排序了吗）
2. 我的历史是 append-only 的吗？（压缩结果会一次性提交吗）
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
| tools 确定性排序 | litellm `Capabilities` 按 API 排序；本项目工具集在 `internal/tools` 侧按固定顺序装配 |
| system 地板断点 | ainovel `internal/agents/build.go` `workerSystem` |
| 滚动尖端 + 回看断点 | agentcore `request.go` `markCache` |
| 末块断点 + 跳 thinking | agentcore `request.go` `withCache` |
| 断点 TTL | ainovel `internal/agents/build.go` `workerCache` / `workerCacheTTL` |
| 能力门控 | ainovel `internal/bootstrap/models.go` `endpoint.mergeOptions` / `acceptsOption` |
| 官方端点判定 + opt-in | ainovel `internal/bootstrap/models.go` `promptCacheKeyAllowed` |
| 缓存身份（一书一基） | ainovel `internal/agents/build.go` `promptCacheBase` |
| 断裂检测 | ainovel `internal/host/usage.go` `noteCacheBreak` |
| 架构定位 | ainovel `docs/architecture.md` §6.6 |
