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
