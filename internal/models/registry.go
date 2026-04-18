// Package models 提供 LLM 模型元数据注册表（上下文窗口、输出上限、价格），
// 数据源 OpenRouter API，编译期基线 + 运行期刷新。
package models

//go:generate go run gen_models.go

import (
	"strings"
	"sync"
)

// ModelEntry 描述一个已知的 LLM 模型。
type ModelEntry struct {
	Provider            string  `json:"provider"`               // OpenRouter 规范化后的厂商名 (anthropic/openai/gemini/...)
	ID                  string  `json:"id"`                     // 模型 ID (不含厂商前缀)
	Name                string  `json:"name"`                   // 展示名
	ContextWindow       int     `json:"context_window"`         // 输入窗口
	MaxTokens           int     `json:"max_tokens"`             // 单次输出上限
	InputCostPer1M      float64 `json:"input_cost_per_1m"`      // 输入价格 (USD/1M tokens)
	OutputCostPer1M     float64 `json:"output_cost_per_1m"`     // 输出价格
	CacheReadCostPer1M  float64 `json:"cache_read_cost_per_1m"` // 缓存读取价格
	CacheWriteCostPer1M float64 `json:"cache_write_cost_per_1m"`
}

// ModelRegistry 保存已知模型，支持模糊解析与运行期合并。
type ModelRegistry struct {
	mu     sync.RWMutex
	models []ModelEntry
}

// NewModelRegistry 返回一个已加载编译期基线的注册表。
func NewModelRegistry() *ModelRegistry {
	r := &ModelRegistry{}
	r.models = append(r.models, generatedModels...)
	return r
}

var (
	defaultRegistry     *ModelRegistry
	defaultRegistryOnce sync.Once
)

// DefaultRegistry 返回全局注册表（懒加载，线程安全）。
// 启动阶段调用 StartPricingRefresh 可让后台刷新价格/窗口信息。
func DefaultRegistry() *ModelRegistry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewModelRegistry()
	})
	return defaultRegistry
}

// Resolve 按照一个模型标识（可能是 "provider/model"、完整 ID、或局部名）查找条目。
//
// 匹配顺序：
//  1. 若包含 "/"，按 "provider/model" 精确查找
//  2. 精确/日期后缀匹配
//  3. 子串匹配（ID 或 Name 包含 pattern）
//
// 命中多个时，优先返回不含日期后缀的别名（例如 claude-sonnet-4 优先于 claude-sonnet-4-20250514）。
func (r *ModelRegistry) Resolve(pattern string) (*ModelEntry, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		if entry, ok := lookupModelEntry(r.models, prov, modelID); ok {
			return &entry, true
		}
		// OpenRouter 的 vendor 前缀（google/、x-ai/）不一定等于本地 Provider 名，
		// 退回仅用 modelID 查，保证 "google/gemini-2.5-pro" 能命中 gemini 条目。
		if entry, ok := lookupModelEntry(r.models, "", modelID); ok {
			return &entry, true
		}
	}

	if entry, ok := lookupModelEntry(r.models, "", pattern); ok {
		return &entry, true
	}

	lower := strings.ToLower(pattern)
	normalized := normalizeModelLookupID(pattern)
	var candidates []int
	for i := range r.models {
		if strings.Contains(normalizeModelLookupID(r.models[i].ID), normalized) ||
			strings.Contains(strings.ToLower(r.models[i].ID), lower) ||
			strings.Contains(strings.ToLower(r.models[i].Name), lower) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}

	best := candidates[0]
	for _, i := range candidates[1:] {
		if !hasDatedSuffix(r.models[i].ID) && hasDatedSuffix(r.models[best].ID) {
			best = i
		}
	}
	entry := r.models[best]
	return &entry, true
}

// ResolveContextWindow 返回某个模型的上下文窗口；未命中返回 0。
func (r *ModelRegistry) ResolveContextWindow(pattern string) int {
	if e, ok := r.Resolve(pattern); ok {
		return e.ContextWindow
	}
	return 0
}

// List 返回所有模型（可选 filter，空字符串表示全量）。
func (r *ModelRegistry) List(filter string) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if filter == "" {
		return append([]ModelEntry{}, r.models...)
	}
	lower := strings.ToLower(filter)
	normalized := normalizeModelLookupID(filter)
	var out []ModelEntry
	for _, m := range r.models {
		if strings.Contains(strings.ToLower(m.Provider), lower) ||
			strings.Contains(normalizeModelLookupID(m.ID), normalized) ||
			strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			out = append(out, m)
		}
	}
	return out
}

// MergeModels 按 provider+id 大小写不敏感合并。
// 非零价格/窗口/MaxTokens/Name 会覆盖已有条目；新增条目直接追加。
func (r *ModelRegistry) MergeModels(fetched []ModelEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := make(map[string]int, len(r.models))
	for i, m := range r.models {
		idx[strings.ToLower(m.Provider+"/"+m.ID)] = i
	}
	for _, f := range fetched {
		key := strings.ToLower(f.Provider + "/" + f.ID)
		if i, ok := idx[key]; ok {
			if f.InputCostPer1M > 0 || f.OutputCostPer1M > 0 {
				r.models[i].InputCostPer1M = f.InputCostPer1M
				r.models[i].OutputCostPer1M = f.OutputCostPer1M
				r.models[i].CacheReadCostPer1M = f.CacheReadCostPer1M
				r.models[i].CacheWriteCostPer1M = f.CacheWriteCostPer1M
			}
			if f.ContextWindow > 0 {
				r.models[i].ContextWindow = f.ContextWindow
			}
			if f.MaxTokens > 0 {
				r.models[i].MaxTokens = f.MaxTokens
			}
			if f.Name != "" {
				r.models[i].Name = f.Name
			}
		} else {
			r.models = append(r.models, f)
			idx[key] = len(r.models) - 1
		}
	}
}

