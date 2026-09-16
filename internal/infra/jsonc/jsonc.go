// Package jsonc 是用户可编辑输入文件的统一解码：先把注释与尾逗号标准化，
// 再按严格模式解码，未知字段一律拒绝。作品投影、Creator Profile、Pack 清单
// 与 eval 共用这一套语义。
package jsonc

import (
	"fmt"
	"os"

	"github.com/tailscale/hujson"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Decode 标准化 JSONC 后严格解码到 target。
func Decode(payload []byte, target any) error {
	standard, err := hujson.Standardize(payload)
	if err != nil {
		return fmt.Errorf("parse JSONC: %w", err)
	}
	return model.DecodeStrict(standard, target)
}

// DecodeFile 读取并解码 JSONC 文件。
func DecodeFile(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JSONC file: %w", err)
	}
	return Decode(payload, target)
}
