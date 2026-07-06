package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxLoadFileSize 限制单次加载的文件大小，避免误读超大文件灌爆 textarea。
// 32KB 大约对应中文 1.5 万字 / 英文 3 万字符，足够覆盖共创导出的 md。
const maxLoadFileSize = 32 * 1024

// resolveProjectPath 把用户输入的路径解析为绝对路径。
//
// 规则：
//   - 以 @ 开头：剥离 @（/rewrite @path 语法糖）
//   - 以 ~/ 开头：展开为 $HOME
//   - 以 ./ 开头或相对路径：相对 projectDir
//   - 绝对路径：原样使用
func resolveProjectPath(projectDir, raw string) (string, error) {
	p := strings.TrimSpace(raw)
	p = strings.TrimPrefix(p, "@")
	if p == "" {
		return "", fmt.Errorf("路径为空")
	}

	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析家目录失败：%w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}

	if filepath.IsAbs(p) {
		return p, nil
	}

	return filepath.Join(projectDir, p), nil
}

// loadFileAsPrompt 读取文件内容并裁剪为可直接灌入 textarea 的字符串。
//
// 错误场景：
//   - 文件不存在：友好提示"文件不存在：<path>"
//   - 超过 maxLoadFileSize：友好提示文件过大
//   - 读取失败：包装原始错误
func loadFileAsPrompt(projectDir, raw string) (string, error) {
	abs, err := resolveProjectPath(projectDir, raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在：%s", abs)
		}
		return "", fmt.Errorf("stat 失败：%w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("目标路径是目录，请指定 .md 文件：%s", abs)
	}
	if info.Size() > maxLoadFileSize {
		return "", fmt.Errorf("文件过大（%d 字节，上限 %d）：%s", info.Size(), maxLoadFileSize, abs)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("读取失败：%w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// loadFileForSend 读取文件内容供 --send 模式直接作为消息提交。
// 与 loadFileAsPrompt 不同：不检查 maxLoadFileSize，因为内容不经过 textarea，
// 而是直接交给 LLM；textarea 的 CharLimit 和文件大小检查本来就是为了保护
// 输入框，--send 路径不需要这层防护。LLM 上下文窗口才是真正上限。
func loadFileForSend(projectDir, raw string) (string, error) {
	abs, err := resolveProjectPath(projectDir, raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在：%s", abs)
		}
		return "", fmt.Errorf("stat 失败：%w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("目标路径是目录，请指定 .md 文件：%s", abs)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("读取失败：%w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
