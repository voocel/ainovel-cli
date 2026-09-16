package bootstrap

import (
	"context"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// FromConfig 按用户配置装配应用：未配置模型时不安装执行器；有配置时装配真实
// capability Runtime。实时活动通道只在交互入口装配（发布与订阅共用同一 Hub），
// 脚本入口保持零活动开销。
func FromConfig(s *store.Store, config appconfig.Config, version string, interactive bool) (*App, error) {
	if !config.Configured() {
		return New(s, Options{Version: version}), nil
	}
	modelConfig, err := executionModelConfig(config)
	if err != nil {
		return nil, err
	}
	modelDigest, err := modelConfig.Digest()
	if err != nil {
		return nil, err
	}
	chat, err := models.New(modelConfig)
	if err != nil {
		return nil, err
	}
	runtime := capability.NewRuntime(chat, modelDigest, s)
	app := New(s, Options{Version: version, Executors: task.ExecutorSet{LLM: runtime}})
	if interactive {
		hub := activity.NewHub()
		runtime.SetActivitySink(hub)
		app.Workbench.AttachActivityFeed(hub)
	}
	return app, nil
}

// VerifyModel 用最小请求验证配置可真实连通：配置向导先验证再落盘，
// 避免把坏配置写进文件导致启动锁死。
func VerifyModel(ctx context.Context, config appconfig.Config) error {
	modelConfig, err := executionModelConfig(config)
	if err != nil {
		return err
	}
	modelConfig.Timeout = 30 * time.Second
	return models.Verify(ctx, modelConfig)
}

// executionModelConfig 把用户拥有的连接名解析成协议适配器配置。
func executionModelConfig(config appconfig.Config) (models.Config, error) {
	pc, err := config.ActiveProvider()
	if err != nil {
		return models.Config{}, err
	}
	return models.Config{Provider: pc.Type, API: pc.API, Model: config.Model, APIKey: pc.APIKey, BaseURL: pc.BaseURL}, nil
}
