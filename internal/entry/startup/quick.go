package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
)

// PrepareQuick 将直接Nhập整理为可进入 Engine 的快速启动计划。
func PrepareQuick(req Request) (Plan, error) {
	prompt := strings.TrimSpace(req.UserPrompt)
	if prompt == "" {
		return Plan{}, fmt.Errorf("prompt is required")
	}
	return Plan{
		Mode:        ModeQuick,
		DisplayName: "快速Bắt đầu",
		StartPrompt: host.BuildStartPrompt(prompt),
	}, nil
}
