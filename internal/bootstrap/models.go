package bootstrap

import (
	"fmt"
	"log"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

// ModelSet 持有按角色分配的模型实例，未配置的角色回退到默认模型。
type ModelSet struct {
	Default agentcore.ChatModel
	models  map[string]agentcore.ChatModel
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	if m, ok := ms.models[role]; ok {
		return m
	}
	return ms.Default
}

// Summary 返回模型分配摘要（供日志使用）。
func (ms *ModelSet) Summary() string {
	var parts []string
	for role, m := range ms.models {
		parts = append(parts, fmt.Sprintf("%s=%s", role, modelName(m)))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("default=%s", modelName(ms.Default))
	}
	return fmt.Sprintf("default=%s %s", modelName(ms.Default), strings.Join(parts, " "))
}

func modelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return "unknown"
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider+model 组合复用同一个实例。
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)

	// 创建默认模型
	defaultPC := cfg.DefaultProviderConfig()
	defaultModel, err := createModelFromConfig(cfg.Provider, cfg.ModelName, defaultPC, cache)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default: defaultModel,
		models:  make(map[string]agentcore.ChatModel),
	}

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q", role, rc.Provider)
		}
		m, err := createModelFromConfig(rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = m
		log.Printf("[config] role %s → provider=%s model=%s", role, rc.Provider, rc.Model)
	}

	return ms, nil
}

// createModelFromConfig 创建或复用 ChatModel 实例。
func createModelFromConfig(providerKey, model string, pc ProviderConfig, cache map[string]agentcore.ChatModel) (agentcore.ChatModel, error) {
	cacheKey := providerKey + "|" + model
	if m, ok := cache[cacheKey]; ok {
		return m, nil
	}

	providerType := pc.ProviderType(providerKey)
	lcfg := litellm.ProviderConfig{APIKey: pc.APIKey}
	if pc.BaseURL != "" {
		lcfg.BaseURL = pc.BaseURL
	}

	client, err := litellm.NewWithProvider(providerType, lcfg)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w", providerKey, providerType, err)
	}

	var m agentcore.ChatModel
	if providerType == "openai" && strings.EqualFold(strings.TrimSpace(pc.WireAPI), "responses") {
		m = newOpenAIResponsesModel(model, client)
	} else {
		m = llm.NewLiteLLMAdapter(model, client)
	}
	cache[cacheKey] = m
	return m, nil
}
