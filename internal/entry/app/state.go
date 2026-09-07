package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State 是跨启动的界面落点与布局偏好（workbench §4/M3）：记录上次打开的
// 作品与各作品大纲的折叠节点。它不是权威数据，丢失只影响呈现。
type State struct {
	LastProjectID string `json:"last_project_id,omitempty"`
	// Collapsed 各作品折叠的大纲节点 ID（键为作品 ID）；已删作品的残留
	// 条目无害，不做清理。
	Collapsed map[string][]string `json:"collapsed,omitempty"`
}

func StatePath(dir string) string { return filepath.Join(dir, "state.json") }

// LoadState 读取启动状态。文件不存在是正常的空状态；其余错误（权限、损坏）
// 伴随空状态返回非 nil error——调用方此时**不得回写**（读改写会把其他作品的
// 偏好一并抹掉），且应把异常告知用户而非静默按空处理。
func LoadState(dir string) (State, error) {
	payload, err := os.ReadFile(StatePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read app state: %w", err)
	}
	var state State
	if err := json.Unmarshal(payload, &state); err != nil {
		return State{}, fmt.Errorf("decode app state: %w", err)
	}
	return state, nil
}

func SaveState(dir string, state State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create app state dir: %w", err)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode app state: %w", err)
	}
	if err := os.WriteFile(StatePath(dir), payload, 0o600); err != nil {
		return fmt.Errorf("write app state: %w", err)
	}
	return nil
}
