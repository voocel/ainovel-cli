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
