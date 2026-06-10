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
