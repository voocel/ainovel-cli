package host

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// 预算Trạng thái机：单调递进，每次迁移恰好触发一次副作用，不回退。
// 上调预算 = 用户重Mới授权 = 改Cấu hình后重启/Mới Host 实例，不在本实例内回退Trạng thái。
const (
	budgetNormal      int32 = iota // 未到告警水位
	budgetWarned                   // 已发告警，未越线
	budgetStopPending              // 已越线，等子Proxy边界停机
	budgetStopped                  // 已执行停机
)

// BudgetSentinel 监视累计成本，执行用户的预算政策（config budget 块）。
//
// 合宪定位（architecture.md §8.3/§10）：不评估Mô hình行为——越线停机等同于用户在
// 那一刻手动 Abort，Host 只是代为执行一条预先签署的指令。它影响控制流，因此
// 不是观察者，定位为与 flow.Dispatcher 平级的 Host 政策组件；Route/工具层不感知。
//
// 停机时机：Mặc định在子Proxy边界（Host 同步调用 HandleBoundary），不浪费 in-flight Chương；
// hardStop=true 时越线立即停。边界处理先于 flow.Dispatcher 派发Bước tiếp，Route/工具层不感知预算。
type BudgetSentinel struct {
	limit     float64
	warnRatio float64
	hardStop  bool

	costNow func() float64              // Hiện tại累计成本（usage.Totals 包装；可注入测试桩）
	abort   func(reason string)         // Host 停机包装（带原因事件）
	report  func(level, summary string) // 告警出口（emitEvent + notify，由 Host 注入）

	state atomic.Int32

	// 计费盲区检测：注册表Không có价且 provider 不自报 cost 的Mô hình每笔记账增量为 $0，
	// 预算静默失效。按"连续多笔零增量"判定而非 total==0——后者抓不住长跑中途
	// /model 切到Không có价Mô hình的Cảnh（total 停在Lịch sử值非零但不再增长）。
	// 免费Mô hình同样命中，提示"预算不会触发"对其同样成立。
	lastTotal   atomic.Uint64 // math.Float64bits(上次回调的累计成本)
	zeroStreak  atomic.Int32
	blindWarned atomic.Bool
}

// blindZeroStreak 连续零增量记账多少笔后告警。正常计价Mô hình每笔增量必 > 0
// （cost 是 float 累计不取整），取 5 仅为避免极端毛刺，不是可调策略阈值。
const blindZeroStreak = 5

// NewBudgetSentinel Tạo预算哨兵；政策未Bật时Quay lại nil（所有方法 nil 安全）。
func NewBudgetSentinel(cfg bootstrap.BudgetConfig, costNow func() float64, abort func(reason string), report func(level, summary string)) *BudgetSentinel {
	if !cfg.Enabled() {
		return nil
	}
	return &BudgetSentinel{
		limit:     cfg.BookUSD,
		warnRatio: cfg.WarnRatio,
		hardStop:  cfg.HardStop,
		costNow:   costNow,
		abort:     abort,
		report:    report,
	}
}

// OnCost 由 UsageTracker 每次记账后携带最Mới累计成本调用（锁外）。
// 一次回调可能连跨两级（normal→warned→stopPending），两次副作用各触发一次。
func (s *BudgetSentinel) OnCost(total float64) {
	if s == nil {
		return
	}
	if prev := s.lastTotal.Swap(math.Float64bits(total)); total == math.Float64frombits(prev) {
		if s.zeroStreak.Add(1) >= blindZeroStreak && s.blindWarned.CompareAndSwap(false, true) {
			s.report("warn", fmt.Sprintf("预算盲区: 连续记账但累计成本停在 $%.2f 不再增长（Hiện tạiMô hình注册表Không có价且 provider 未自报 cost，或为免费Mô hình）——预算上限不会触发", total))
		}
	} else {
		s.zeroStreak.Store(0)
	}
	if total >= s.limit*s.warnRatio && s.state.CompareAndSwap(budgetNormal, budgetWarned) {
		s.report("warn", fmt.Sprintf("预算告警: 已花费 $%.2f，达到预算 $%.2f 的 %.0f%%", total, s.limit, s.warnRatio*100))
	}
	if total >= s.limit && s.state.CompareAndSwap(budgetWarned, budgetStopPending) {
		if s.hardStop {
			s.report("error", fmt.Sprintf("预算用尽: 已花费 $%.2f，超出预算 $%.2f，立即停机", total, s.limit))
			s.stop(total)
			return
		}
		s.report("error", fmt.Sprintf("预算用尽: 已花费 $%.2f，超出预算 $%.2f，将在Hiện tại子Proxy任务Kết thúc后停机", total, s.limit))
	}
}

// HandleEvent 在子Proxy边界执行待定的停机。订阅必须先于 Dispatcher。
// 不Bỏ qua IsError——出错Quay lại同样是边界，停机不应因子ProxyThất bại而推迟。
func (s *BudgetSentinel) HandleEvent(ev agentcore.Event) {
	if s == nil {
		return
	}
	if ev.Type != agentcore.EventToolExecEnd || ev.Tool != "subagent" {
		return
	}
	s.HandleBoundary()
}

func (s *BudgetSentinel) HandleBoundary() bool {
	if s == nil || s.state.Load() != budgetStopPending {
		return false
	}
	s.stop(s.costNow())
	return true
}

func (s *BudgetSentinel) stop(total float64) {
	if s.state.CompareAndSwap(budgetStopPending, budgetStopped) {
		s.abort(fmt.Sprintf("预算停机: 已花费 $%.2f，超出预算 $%.2f；上调 budget.book_usd 后可Phục hồi续跑", total, s.limit))
	}
}

// Refuse 启动前置Kiểm tra：预算已超Quay lại拒绝Lỗi（Start/Resume/Continue Phục hồiĐường dẫn调用）。
// 用户上调预算 = 重Mới授权，MớiCấu hình下 Refuse 自然放行。
func (s *BudgetSentinel) Refuse() error {
	if s == nil {
		return nil
	}
	if cost := s.costNow(); cost >= s.limit {
		return fmt.Errorf("本书已花费 $%.2f，达到预算上限 $%.2f；Vui lòng上调Cấu hình budget.book_usd 后Thử lại", cost, s.limit)
	}
	return nil
}

// Limit Quay lại预算上限（UI 展示用）；未BậtQuay lại 0。
func (s *BudgetSentinel) Limit() float64 {
	if s == nil {
		return 0
	}
	return s.limit
}
