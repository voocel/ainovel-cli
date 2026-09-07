package service

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// NewID 生成人类可读且防碰撞的标识（时间戳 + 24 位随机后缀）：
// 作品与变更的身份由服务层统一定义，入口层不自行拼装。
func NewID(prefix string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%06x", prefix, now.UTC().Format("20060102-150405"), rand.Uint32()&0xffffff)
}
