package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SaveCoCreateDoc 把 AI 整理出的创作指令草稿保存为 Markdown 文件。
//
// 落盘位置：<project>/meta/cocreate/cocreate-YYYYMMDD-HHMMSS.md
// 同一秒内多次保存：追加 -2、-3 后缀避免覆盖。
//
// content 为空时返回错误（友好提示），不创建空文件。
func (h *Host) SaveCoCreateDoc(content string) (string, error) {
	if h == nil {
		return "", fmt.Errorf("host 未初始化")
	}
	draft := strings.TrimSpace(content)
	if draft == "" {
		return "", fmt.Errorf("AI 还未整理出创作指令，暂无可导出内容")
	}

	dir := filepath.Join(h.Dir(), "meta", "cocreate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败：%w", err)
	}

	base := "cocreate-" + time.Now().Format("20060102-150405")
	name := base + ".md"
	for i := 2; ; i++ {
		full := filepath.Join(dir, name)
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				name = fmt.Sprintf("%s-%d.md", base, i)
				continue
			}
			return "", fmt.Errorf("写入失败：%w", err)
		}
		if _, err := f.WriteString(draft + "\n"); err != nil {
			f.Close()
			return "", fmt.Errorf("写入失败：%w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("关闭文件失败：%w", err)
		}
		return full, nil
	}
}
