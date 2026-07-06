package host

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RewriteFoundation 把所有 foundation 数据归档到
// meta/rewrite-backup-YYYYMMDD-HHMMSS/，再把项目重置回"未启动"状态。
//
// 归档清单：
//   - premise.md / outline.json / outline.md
//   - layered_outline.json / layered_outline.md
//   - characters.json / characters.md
//   - world_rules.json / world_rules.md
//   - meta/compass.json / meta/progress.json
//   - chapters/ 整个目录（已完成章节正文）
//
// 调用后 phase 回到空（未启动），architect 可重新被启动跑大纲。
//
// 该方法只能在 lifecycle != lifecycleRunning 且非阶段共创时调用；
// 违反时返回错误，避免与正在跑的 agent 冲突。
//
// 不删除：meta/sessions/（调用历史，便于审计）、meta/runtime/（运行队列，重启时
// 会清空）、meta/materials.json（素材库与大纲解耦，单独保留）。
func (h *Host) RewriteFoundation() (backupDir string, err error) {
	if h == nil {
		return "", fmt.Errorf("host 未初始化")
	}
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("创作进行中，请先 Esc 暂停或等待完成后再重写")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("阶段共创进行中，请先结束共创再重写")
	}
	h.mu.Unlock()

	stamp := time.Now().Format("20060102-150405")
	rel := filepath.Join("meta", "rewrite-backup-"+stamp)
	dir := filepath.Join(h.Dir(), rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建备份目录失败：%w", err)
	}

	// 同秒内重复执行：把已有目录改名作为兜底（理论上前置约束会拦住，但保险一点）
	for i := 2; ; i++ {
		alt := dir + fmt.Sprintf("-%d", i)
		if _, err := os.Stat(alt); os.IsNotExist(err) {
			break
		}
	}

	// 文件 → 备份目录根；目录（chapters/）→ 备份目录下的同名子目录
	type moveTarget struct {
		rel string
		as  string
	}
	files := []moveTarget{
		{"premise.md", "premise.md"},
		{"outline.json", "outline.json"},
		{"outline.md", "outline.md"},
		{"layered_outline.json", "layered_outline.json"},
		{"layered_outline.md", "layered_outline.md"},
		{"characters.json", "characters.json"},
		{"characters.md", "characters.md"},
		{"world_rules.json", "world_rules.json"},
		{"world_rules.md", "world_rules.md"},
		{"meta/compass.json", "compass.json"},
		{"meta/progress.json", "progress.json"},
	}
	dirs := []moveTarget{
		{"chapters", "chapters"},
	}

	for _, f := range files {
		src := filepath.Join(h.Dir(), f.rel)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return dir, fmt.Errorf("stat %s 失败：%w", f.rel, err)
		}
		dst := filepath.Join(dir, f.as)
		if err := os.Rename(src, dst); err != nil {
			return dir, fmt.Errorf("归档 %s 失败：%w", f.rel, err)
		}
	}
	for _, d := range dirs {
		src := filepath.Join(h.Dir(), d.rel)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return dir, fmt.Errorf("stat %s 失败：%w", d.rel, err)
		}
		dst := filepath.Join(dir, d.as)
		if err := os.Rename(src, dst); err != nil {
			return dir, fmt.Errorf("归档 %s 失败：%w", d.rel, err)
		}
	}

	// 重建空 chapters/ 目录 + 重置 progress（保持 store 内部状态一致）
	if err := h.store.Init(); err != nil {
		return dir, fmt.Errorf("重建目录失败：%w", err)
	}
	if err := h.store.Progress.Init("", 0); err != nil {
		return dir, fmt.Errorf("重置 progress 失败：%w", err)
	}

	return dir, nil
}
