package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestStartPreparedWithGenreRejectsInvalidBeforeHostWork(t *testing.T) {
	// 最小 Host 没有 store/model。非法类型必须在访问这些依赖或重置 checkpoint 前返回。
	h := &Host{}
	err := h.StartPreparedWithGenre("写一个故事", domain.Genre("screenplay"))
	if err == nil || !strings.Contains(err.Error(), "invalid genre") {
		t.Fatalf("非法作品类型应在启动前被拒绝，得到 %v", err)
	}
}
