package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeStrict 把 content 解码进 target：拒绝未知字段，且必须恰好一个 JSON 值。
// 权威文档、工具参数、模型结构化回复与用户文件共用这一条纪律，不允许各层
// 各写一套宽严不一的解码器。
func DecodeStrict(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON must contain exactly one value: %w", ErrInvalid)
	}
	return nil
}

// Digest 是内容身份的唯一摘要算法（sha256 hex）：幂等比对、缓存键与快照
// 摘要都以它为准，换算法即换身份。
func Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// DigestJSON 对 value 的 JSON 编码取摘要。
func DigestJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return Digest(payload), nil
}
