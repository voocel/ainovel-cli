package host

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/models"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// recentSampleCap 是滑动窗大小：只保留每个 role 最近 N 次调用的 (cacheRead, input)
// 样本，用于在左栏对比"累计 vs 近 N 次"命中率，识别"前期拖累"vs"稳态低命中"。
const recentSampleCap = 10

// 缓存链断裂判定双阈值（对齐 Claude Code 的实证经验）：命中量较上次下降超过
// 5%（相对）且降幅 ≥2000 tokens（绝对）才算断裂——单一相对阈值会被小前缀噪声
// 淹没，单一绝对阈值会漏掉大前缀的显著退化。
const (
	cacheBreakKeepRatio     = 0.95
	cacheBreakMinDropTokens = 2000
)

// UsageTracker 累计整个会话所有 agent 的 LLM 输入/输出 token 与美元成本。
//
// 工作机制：
//   - 每次 agent 的 OnMessage 回调触发时调用 Record(agentName, msg)
//   - agentName 映射到 role（architect_* 归一为 architect），查 ModelSet 当前该 role 绑定的模型
//   - 用 models.DefaultRegistry 查模型价格，按非缓存输入/输出/缓存读/缓存写四项累乘
//   - 注册表无此模型时，退回 msg.Usage.Cost.Total（provider 自带，可能为 0）
//   - 模型热切换（/model）后续消息自动按新模型算价，旧消息保留旧成本
//
// 同时维护 per-role 维度（writer/editor/architect）：
//   - 累计命中数据 → 整体优化效果
//   - 滑动窗最近 N 次 → 区分前期拖累 vs 稳态低命中
//   - CacheCapable 标记 → 区分"未启用"和"真的 0% 命中"
//
// 线程安全。
type UsageTracker struct {
	mu       sync.Mutex
	overall  agentTotals
	perAgent map[string]*agentTotals // key 为 agentRoleName 归一后的 role 名
	perModel map[string]*agentTotals // key 为 provider/model；provider 未知时退化为 model
	modelSet *bootstrap.ModelSet
	store    *storepkg.Store // 可为 nil（测试场景），nil 时所有持久化方法静默 noop

	// cacheTrack 是 per-role 的缓存链基线（上次调用的前缀长度/命中量/时间），
	// 用于断裂检测。只在 live Record 路径更新——replay 重放历史不检测，
	// 否则每次启动都会把陈年断裂刷成误报。不持久化。
	cacheTrack map[string]*cacheTrackState

	// missingAssistantUsage 累计"收到 assistant 消息但 Usage 为 nil"的次数。
	// 实测下来主要发生在自建 OpenAI 兼容 backend 没在 streaming 末尾按 OpenAI
	// stream_options.include_usage 协议发那条 final usage chunk 时——partial.Usage
	// 始终为 nil，所有累计字段全部停在 0。计数器让 UI 能直接告诉用户"是上游不返
	// usage 不是这边坏了"，而不是死磕缓存面板代码。
	missingAssistantUsage int
	loggedMissingUsage    bool // 整个会话只 warn 一次，避免 tui.log 被刷屏

	// saveCh 由 Record 在累加后非阻塞触发；autoSaveLoop 监听并按 debounce 落盘。
	// buffered=1：连续多次 Record 折叠为一次落盘信号；满了直接丢，下个 tick 一并写。
	saveCh       chan struct{}
	autoSaveMu   sync.Mutex
	autoSaveDone chan struct{}

	// onCost 在每次记账后于锁外携带最新累计成本调用（BudgetSentinel 越线检测）。
	// 必须在并发 Record 开始前通过 SetOnCost 设置，之后只读。
	onCost func(total float64)

	// onMissingUsage 在首次发现"assistant 消息无 Usage"时调用一次（与 slog warn
	// 同时机）。预算启用时这意味着计费盲区——成本恒 0、预算永不触发，必须喊人。
	onMissingUsage func()
}

// usageSample 是单次 OnMessage 的命中样本，仅记录命中率分子分母。
type usageSample struct {
	CacheRead int
	Input     int
}

// cacheTrackState 是一个 role 当前会话的缓存链基线。task（spawn 任务文本）是
// 会话身份：换 task = 新 spawn = 新缓存血统（prompt_cache_key 带 #seq），首请求
// 命中低是常态，直接换基线不比较——否则"上一会话很短、新会话首请求前缀反而更长"
// 时会误报断裂。Input 语义（含 CacheRead，见 computeCost 注释）恰好等于"服务端
// 处理的前缀长度"，据此可区分三种走向：前缀缩短 = 会话内压缩（合法，重置基线）；
// 前缀增长且命中跟涨 = 链路健康；前缀增长而命中骤降 = 断裂。
type cacheTrackState struct {
	task          string
	lastPrefix    int
	lastCacheRead int
	lastAt        time.Time
}

// agentTotals 是一个 agent 的累计计数。
//   - Saved 是按当前命中数据反算的"如果按非缓存价计费"的差额
//   - CacheCapable 仅在该 role 至少经过一次"已知支持 cache 的模型"调用后置 true
//   - samples 是定长 ring buffer，前 recentSampleCap 次直接追加，之后按 sampleIdx 轮转
type agentTotals struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite   int
	Cost         float64
	Saved        float64
	CacheCapable bool
	CacheBreaks  int // live 检测到的缓存链断裂次数（replay 不计）
	samples      []usageSample
	sampleIdx    int
}

func NewUsageTracker(set *bootstrap.ModelSet, store *storepkg.Store) *UsageTracker {
	return &UsageTracker{
		modelSet:   set,
		store:      store,
		perAgent:   make(map[string]*agentTotals, 4),
		perModel:   make(map[string]*agentTotals, 4),
		cacheTrack: make(map[string]*cacheTrackState, 4),
		saveCh:     make(chan struct{}, 1),
	}
}

// Record 把一条 agent 消息分发到累加 / 诊断两条路径。
//
// 累加只看 Usage 是否存在——"哪条消息带 Usage" 是 agentcore/litellm adapter
// 装配细节（上游协议把 usage 放在响应顶层），未来装配规则变了也不用动这里。
// 诊断要求 Role=Assistant 且 Content 非空，避免 AbortMsg / 异常恢复 / tool /
// user 消息污染 missingAssistantUsage 计数。
func (t *UsageTracker) Record(agentName, task string, msg agentcore.AgentMessage) {
	if t == nil {
		return
	}
	m, ok := msg.(agentcore.Message)
	if !ok {
		return
	}
	if m.Usage == nil {
		if m.Role == agentcore.RoleAssistant && len(m.Content) > 0 {
			t.flagMissingUsage(agentName)
		}
		return
	}
	role := agentRoleName(agentName)
	t.noteCacheBreak(role, task, *m.Usage)
	provider, modelName := usageActualModel(m.Usage)
	t.accumulate(role, provider, modelName, *m.Usage)
}

// noteCacheBreak 是缓存链断裂检测（纯观测，不修复，只在 live Record 路径调用）。
//
// 判定：同一会话（role+task）内前缀（Input，含 CacheRead）未缩短，而命中量较上次
// 下降 >5% 且降幅 ≥2000 tokens。task 变化 = 新 spawn = 新缓存血统，直接换基线不
// 比较；前缀缩短说明是上下文压缩，属合法下降，只重置基线不告警。归因按优先级给
// 提示：间隔超过 TTL → 疑似过期；间隔很短且客户端字节本应稳定 → 疑似服务端逐出/
// 路由漂移（中转站轮询上游是常见原因）。
func (t *UsageTracker) noteCacheBreak(role, task string, u agentcore.Usage) {
	now := time.Now()
	prefix := u.Input // litellm 各 provider 保证 Input 含 CacheRead

	t.mu.Lock()
	st := t.cacheTrack[role]
	if st == nil || st.task != task {
		t.cacheTrack[role] = &cacheTrackState{task: task, lastPrefix: prefix, lastCacheRead: u.CacheRead, lastAt: now}
		t.mu.Unlock()
		return
	}
	prevPrefix, prevRead, prevAt := st.lastPrefix, st.lastCacheRead, st.lastAt
	st.lastPrefix, st.lastCacheRead, st.lastAt = prefix, u.CacheRead, now

	broke := prevPrefix > 0 && prefix >= prevPrefix &&
		float64(u.CacheRead) < float64(prevRead)*cacheBreakKeepRatio &&
		prevRead-u.CacheRead >= cacheBreakMinDropTokens
	if broke {
		t.overall.CacheBreaks++
		per := t.perAgent[role]
		if per == nil {
			per = &agentTotals{}
			t.perAgent[role] = per
		}
		per.CacheBreaks++
	}
	t.mu.Unlock()

	if !broke {
		return
	}
	gap := now.Sub(prevAt).Round(time.Second)
	hint := "疑似服务端逐出/路由漂移（中转站轮询上游是常见原因）"
	if gap > time.Hour {
		hint = "疑似 1h TTL 过期"
	} else if gap > 5*time.Minute {
		hint = "疑似 5m TTL 过期"
	}
	slog.Warn("缓存链断裂：前缀未缩短而命中骤降",
		"module", "usage", "role", role,
		"cache_read", fmt.Sprintf("%d→%d", prevRead, u.CacheRead),
		"prefix", fmt.Sprintf("%d→%d", prevPrefix, prefix),
		"gap", gap.String(), "hint", hint)
	t.notifyDirty()
}

func usageActualModel(u *agentcore.Usage) (provider, modelName string) {
	if u == nil {
		return "", ""
	}
	return strings.TrimSpace(u.Provider), strings.TrimSpace(u.Model)
}

// flagMissingUsage 累计一次"看似真 LLM 响应却没拿到 usage"事件，整会话只打一次
// warn 日志避免 tui.log 被刷屏。
func (t *UsageTracker) flagMissingUsage(agentName string) {
	t.mu.Lock()
	t.missingAssistantUsage++
	shouldLog := !t.loggedMissingUsage
	t.loggedMissingUsage = true
	t.mu.Unlock()
	if shouldLog {
		slog.Warn("LLM 响应未携带 usage 数据，缓存/成本面板将无累计——通常是上游 streaming 未按 OpenAI include_usage 协议发 final usage chunk",
			"module", "usage", "agent", agentName)
		if t.onMissingUsage != nil {
			t.onMissingUsage()
		}
	}
	t.notifyDirty()
}

// SetOnMissingUsage 注册"首次发现 usage 缺失"的一次性回调。
// 必须在 Host 构造期、并发 Record 开始前调用一次。
func (t *UsageTracker) SetOnMissingUsage(cb func()) {
	if t == nil {
		return
	}
	t.onMissingUsage = cb
}

// notifyDirty 非阻塞触发一次落盘信号，由 autoSaveLoop 按 debounce 实际写入。
// 信号通道 buffered=1：连续多次 Record 折叠成一次保存请求即可。
func (t *UsageTracker) notifyDirty() {
	if t == nil || t.saveCh == nil {
		return
	}
	select {
	case t.saveCh <- struct{}{}:
	default:
	}
}

// accumulate 把一条带 Usage 的消息累计到 overall / per-role / per-model 三份计数。
// provider/model 为空表示"用当前 ModelSet 拿 role 对应模型"（实时路径）；非空表示
// "强制按指定模型算价"（replay 路径用 session jsonl 里的 _meta）。
// resolveCost 在锁外执行（它只读 modelSet/Registry），锁内只做加法。
func (t *UsageTracker) accumulate(role, provider, modelName string, u agentcore.Usage) {
	provider, modelName = t.effectiveModel(role, provider, modelName)
	cost, saved, capable := t.resolveCost(modelName, u)

	t.mu.Lock()
	addUsage(&t.overall, u, cost, saved, capable)

	per := t.perAgent[role]
	if per == nil {
		per = &agentTotals{}
		t.perAgent[role] = per
	}
	addUsage(per, u, cost, saved, capable)

	if key := modelUsageKey(provider, modelName); key != "" {
		perModel := t.perModel[key]
		if perModel == nil {
			perModel = &agentTotals{}
			t.perModel[key] = perModel
		}
		addUsage(perModel, u, cost, saved, capable)
	}
	total := t.overall.Cost
	t.mu.Unlock()

	t.notifyDirty()
	if t.onCost != nil {
		t.onCost(total)
	}
}

// SetOnCost 注册记账回调（携带最新累计成本，锁外调用）。
// 必须在 Host 构造期、并发 Record 开始前调用一次。
func (t *UsageTracker) SetOnCost(cb func(total float64)) {
	if t == nil {
		return
	}
	t.onCost = cb
}

func (t *UsageTracker) effectiveModel(role, provider, modelName string) (string, string) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		if t != nil && t.modelSet != nil {
			p, m, _ := t.modelSet.CurrentSelection(role)
			return p, m
		}
		return "", ""
	}
	if provider == "" && t != nil && t.modelSet != nil {
		p, m, _ := t.modelSet.CurrentSelection(role)
		if m == modelName {
			provider = p
		}
	}
	return provider, modelName
}

func modelUsageKey(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	switch {
	case modelName == "":
		return ""
	case provider == "":
		return modelName
	default:
		return provider + "/" + modelName
	}
}

// addUsage 把单次调用的 token 与成本叠加到一份 totals 上。
// 必须在持有 UsageTracker.mu 的情况下调用。
//
// CacheCapable 优先用"事实"判定：只要见过 CacheRead 或 CacheWrite > 0，就证明
// 上游确实做了 prompt caching。注册表的 CacheReadCostPer1M 仅作 fallback，
// 因为自建 backend 模型（mimo-v2.5-pro / 国内代理等）通常不在 BerriAI/litellm
// pricing 索引里，但实际 Usage 里完全有 cache 数据，UI 不该误判为"未启用"。
func addUsage(t *agentTotals, u agentcore.Usage, cost, saved float64, capable bool) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Cost += cost
	t.Saved += saved
	if capable || u.CacheRead > 0 || u.CacheWrite > 0 {
		t.CacheCapable = true
	}
	pushSample(t, u.CacheRead, u.Input)
}

// pushSample 向 ring buffer 推一个样本。前 recentSampleCap 次纯 append，之后轮转覆盖。
func pushSample(t *agentTotals, cacheRead, input int) {
	s := usageSample{CacheRead: cacheRead, Input: input}
	if len(t.samples) < recentSampleCap {
		t.samples = append(t.samples, s)
		return
	}
	t.samples[t.sampleIdx] = s
	t.sampleIdx = (t.sampleIdx + 1) % recentSampleCap
}

// recentSums 返回滑动窗内 cacheRead 和 input 的总和，作为"近 N 次命中率"的分子分母。
// 用 sum/sum 而非"单次比率的平均"以避免小样本（input=几百 token）放大噪声。
func recentSums(t *agentTotals) (cacheRead, input int) {
	for _, s := range t.samples {
		cacheRead += s.CacheRead
		input += s.Input
	}
	return cacheRead, input
}

// Totals 返回累计总量的快照。
func (t *UsageTracker) Totals() (cost float64, input, output, cacheRead, cacheWrite int) {
	if t == nil {
		return 0, 0, 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Cost, t.overall.Input, t.overall.Output, t.overall.CacheRead, t.overall.CacheWrite
}

// SavedUSD 返回因缓存命中节省的累计美元数。
func (t *UsageTracker) SavedUSD() float64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Saved
}

// OverallRecent 返回滑动窗内（≤ recentSampleCap 次）的 cacheRead 总和、input 总和、样本数。
func (t *UsageTracker) OverallRecent() (cacheRead, input, samples int) {
	if t == nil {
		return 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r, in := recentSums(&t.overall)
	return r, in, len(t.overall.samples)
}

// OverallCacheBreaks 返回 live 检测到的缓存链断裂总次数。
func (t *UsageTracker) OverallCacheBreaks() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheBreaks
}

// OverallCacheCapable 整体是否至少经过一次已知支持 cache 的模型。
func (t *UsageTracker) OverallCacheCapable() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheCapable
}

// MissingAssistantUsage 返回累计"收到 assistant 消息但 Usage 为 nil"的次数。
// 大于 0 通常意味着上游 streaming 没发 OpenAI 的 final usage chunk，
// UI 据此显示提示而非误以为缓存模块本身坏了。
func (t *UsageTracker) MissingAssistantUsage() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.missingAssistantUsage
}

// ── 持久化 ──

// Snapshot 拷贝当前累计状态为可序列化的 domain.UsageState。
// 滑动窗 samples 不进 snapshot——它是短期诊断窗口，跨进程意义不大。
func (t *UsageTracker) Snapshot() domain.UsageState {
	if t == nil {
		return domain.UsageState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := domain.UsageState{
		Schema:       domain.UsageSchemaVersion,
		UpdatedAt:    time.Now(),
		Overall:      totalsSnapshot(&t.overall),
		PerAgent:     make(map[string]domain.AgentUsageTotals, len(t.perAgent)),
		PerModel:     make(map[string]domain.AgentUsageTotals, len(t.perModel)),
		MissingUsage: t.missingAssistantUsage,
	}
	for role, v := range t.perAgent {
		state.PerAgent[role] = totalsSnapshot(v)
	}
	for model, v := range t.perModel {
		state.PerModel[model] = totalsSnapshot(v)
	}
	return state
}

// LoadFromStore 从 store.Usage 读取持久化的快照并回填到内存。返回 true 表示
// 成功加载到了一份非空（schema 匹配）的状态；false 表示无文件或不可用，调用方
// 应继续走 session replay 一次性回填。
func (t *UsageTracker) LoadFromStore() (bool, error) {
	if t == nil || t.store == nil {
		return false, nil
	}
	state, err := t.store.Usage.Load()
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	t.applyState(*state)
	return true, nil
}

// SaveNow 立刻把当前 snapshot 落盘。autoSaveLoop / Close 路径都通过它写。
func (t *UsageTracker) SaveNow() error {
	if t == nil || t.store == nil {
		return nil
	}
	return t.store.Usage.Save(t.Snapshot())
}

// StartAutoSave 起一个 goroutine，监听 saveCh + debounce 落盘。ctx done 前会
// 把最后一次未保存的状态 flush 出去。Close 通过 cancel ctx 触发 flush + 退出。
func (t *UsageTracker) StartAutoSave(ctx context.Context) {
	if t == nil || t.store == nil {
		return
	}
	done := make(chan struct{})
	t.autoSaveMu.Lock()
	t.autoSaveDone = done
	t.autoSaveMu.Unlock()
	go func() {
		defer close(done)
		t.autoSaveLoop(ctx)
	}()
}

// WaitAutoSave 等待取消后的最后一次 flush 完成。Host.Close 先调用 cancel，
// 再等待这里，避免 autoSaveLoop 与退出前 SaveNow 并发写同一快照。
func (t *UsageTracker) WaitAutoSave() {
	if t == nil {
		return
	}
	t.autoSaveMu.Lock()
	done := t.autoSaveDone
	t.autoSaveMu.Unlock()
	if done != nil {
		<-done
	}
}

// autoSaveLoop 把高频 dirty 信号节流为 500ms 一次的落盘。
//
// 设计说明：500ms 是经验值——每章 1-2 个 LLM turn，落盘 1-2 次完全可接受；
// 即便用户手动 ctrl+C 退出来不及触发 timer，ctx 取消路径也会 flush 最后一次。
// 真正的崩溃（OS kill -9）会丢最近 0.5s 内的累计——上游 session jsonl 仍是
// 完整事实，下次启动会从 sessions/ replay 修补差额。
func (t *UsageTracker) autoSaveLoop(ctx context.Context) {
	const debounce = 500 * time.Millisecond
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	var pending bool
	flush := func() {
		if err := t.SaveNow(); err != nil {
			slog.Warn("usage 落盘失败", "module", "usage", "err", err)
		}
		pending = false
	}
	for {
		select {
		case <-ctx.Done():
			if pending {
				flush()
			}
			return
		case <-t.saveCh:
			if pending {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			timer.Reset(debounce)
			pending = true
		case <-timer.C:
			flush()
		}
	}
}

// applyState 把持久化快照写回内存。仅在启动时调用（LoadFromStore / replay 后），
// 此时尚未启动 autoSaveLoop / Record 也不会并发触发，可不持锁；但保留 mu 以防
// 测试或未来调用顺序变化引入并发。
func (t *UsageTracker) applyState(state domain.UsageState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.overall = totalsFromState(state.Overall)
	if state.PerAgent == nil {
		t.perAgent = make(map[string]*agentTotals, 4)
	} else {
		t.perAgent = make(map[string]*agentTotals, len(state.PerAgent))
		for role, v := range state.PerAgent {
			tot := totalsFromState(v)
			t.perAgent[role] = &tot
		}
	}
	if state.PerModel == nil {
		t.perModel = make(map[string]*agentTotals, 4)
	} else {
		t.perModel = make(map[string]*agentTotals, len(state.PerModel))
		for model, v := range state.PerModel {
			tot := totalsFromState(v)
			t.perModel[model] = &tot
		}
	}
	t.missingAssistantUsage = state.MissingUsage
}

// totalsSnapshot 把内存 agentTotals 拷贝成可持久化 domain.AgentUsageTotals。
// samples ring buffer 故意不带出去——见 UsageState 注释。
func totalsSnapshot(t *agentTotals) domain.AgentUsageTotals {
	if t == nil {
		return domain.AgentUsageTotals{}
	}
	return domain.AgentUsageTotals{
		Input:        t.Input,
		Output:       t.Output,
		CacheRead:    t.CacheRead,
		CacheWrite:   t.CacheWrite,
		Cost:         t.Cost,
		Saved:        t.Saved,
		CacheCapable: t.CacheCapable,
		CacheBreaks:  t.CacheBreaks,
	}
}

// totalsFromState 把持久化形态还原为内存 agentTotals。samples 留空，重启后
// 重新从 0 开始积累，几轮 Record 后即可恢复"近 N 次命中率"语义。
func totalsFromState(s domain.AgentUsageTotals) agentTotals {
	return agentTotals{
		Input:        s.Input,
		Output:       s.Output,
		CacheRead:    s.CacheRead,
		CacheWrite:   s.CacheWrite,
		Cost:         s.Cost,
		Saved:        s.Saved,
		CacheCapable: s.CacheCapable,
		CacheBreaks:  s.CacheBreaks,
	}
}

// AgentUsage 是一个 agent 的累计用量快照（向 UI 暴露）。
type AgentUsage struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// PerAgent 返回各 role 累计用量。结果按 CacheRead 数量降序，未消费过 token 的 role 跳过。
func (t *UsageTracker) PerAgent() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perAgent))
	for role, v := range t.perAgent {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		recentRead, recentInput := recentSums(v)
		out = append(out, AgentUsage{
			Role:            role,
			Input:           v.Input,
			Output:          v.Output,
			CacheRead:       v.CacheRead,
			CacheWrite:      v.CacheWrite,
			Cost:            v.Cost,
			Saved:           v.Saved,
			CacheCapable:    v.CacheCapable,
			RecentCacheRead: recentRead,
			RecentInput:     recentInput,
			RecentSamples:   len(v.samples),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CacheRead != out[j].CacheRead {
			return out[i].CacheRead > out[j].CacheRead
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// PerModel 返回各模型累计用量。结果按成本降序，其次按输入量降序。
func (t *UsageTracker) PerModel() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perModel))
	for model, v := range t.perModel {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		out = append(out, AgentUsage{
			Model:        model,
			Input:        v.Input,
			Output:       v.Output,
			CacheRead:    v.CacheRead,
			CacheWrite:   v.CacheWrite,
			Cost:         v.Cost,
			Saved:        v.Saved,
			CacheCapable: v.CacheCapable,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// resolveCost 同时返回本次消息的 cost / saved / capable。
//   - cost: 注册表命中按 4 项累乘；未命中回落 provider 自带 cost
//   - saved: 仅注册表命中、CacheRead > 0、且 InputCost > CacheReadCost 时 > 0
//   - capable: 注册表命中且该模型 CacheReadCostPer1M > 0 → 已知支持 prompt caching
//
// modelName 优先用调用方传入的（replay 时来自 session jsonl 的 _meta.model）。
func (t *UsageTracker) resolveCost(modelName string, u agentcore.Usage) (cost, saved float64, capable bool) {
	if entry, ok := models.DefaultRegistry().Resolve(modelName); ok {
		c := computeCost(u, *entry)
		s := computeSaved(u, *entry)
		canCache := entry.CacheReadCostPer1M > 0
		if c > 0 {
			return c, s, canCache
		}
	}
	if u.Cost != nil {
		return u.Cost.Total, 0, false
	}
	return 0, 0, false
}

// agentRoleName 把 subagent 名字归一到 role 名。
// architect_short/mid/long 都归到 architect；其他原样返回。
func agentRoleName(agentName string) string {
	if strings.HasPrefix(agentName, "architect_") {
		return "architect"
	}
	return agentName
}

// computeCost 按 $/1M tokens 单价计算本次调用的美元开销。
//
// 语义前提（由 litellm 各 provider 统一保证，参见 anthropic.go / bedrock.go /
// openai.go / gemini.go / compat.go 的 Usage 装配点）：
//
//	u.Input  = 全部输入 token，**包含** CacheRead；不含 CacheWrite
//	u.Output = 输出 token
//
// 因此 nonCachedInput = u.Input - u.CacheRead 在所有 provider 都成立。
// 兜底分支保留是为了应对未来某个 provider 误返脏数据时不至于崩。
func computeCost(u agentcore.Usage, e models.ModelEntry) float64 {
	nonCachedInput := u.Input - u.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = u.Input
	}
	c := 0.0
	c += float64(nonCachedInput) * e.InputCostPer1M / 1_000_000
	c += float64(u.Output) * e.OutputCostPer1M / 1_000_000
	c += float64(u.CacheRead) * e.CacheReadCostPer1M / 1_000_000
	c += float64(u.CacheWrite) * e.CacheWriteCostPer1M / 1_000_000
	return c
}

// computeSaved 估算 CacheRead 命中相对于"按普通输入价计费"省下的美元。
// 注意 CacheWrite 的溢价不抵扣 — 它属于"为后续命中铺路"的必要投入，
// 真实收益靠后续 CacheRead 累计回收。
func computeSaved(u agentcore.Usage, e models.ModelEntry) float64 {
	if u.CacheRead <= 0 || e.InputCostPer1M <= 0 {
		return 0
	}
	delta := e.InputCostPer1M - e.CacheReadCostPer1M
	if delta <= 0 {
		return 0
	}
	return float64(u.CacheRead) * delta / 1_000_000
}
