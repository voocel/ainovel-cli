package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Setup 初始化 slog 默认 logger。
// w 为日志输出目标，level 为最低日志级别。
func Setup(w io.Writer, level slog.Level) {
	slog.SetDefault(slog.New(newTextHandler(w, level)))
}

func newTextHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 时间只保留时分秒，节省空间
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})
}

// FileLogger 返回写入 outputDir/logs/filename 的独立 logger 与清理函数，
// 供需要独立日志文件的子系统（如导入流程）使用。打开失败回退默认 logger 不中断业务，
// 但错误必须返回给调用方向用户呈现——否则 UI 指引用户去看一个并不存在的日志文件。
func FileLogger(outputDir, filename string) (*slog.Logger, func(), error) {
	logPath := filepath.Join(outputDir, "logs", filename)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return slog.Default(), func() {}, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return slog.Default(), func() {}, err
	}
	return slog.New(newTextHandler(f, slog.LevelDebug)), func() { _ = f.Close() }, nil
}

// SetupFile 初始化日志到文件，返回清理函数。
// alsoStderr=true 时同时输出到 stderr。
func SetupFile(outputDir, filename string, alsoStderr bool) func() {
	logPath := filepath.Join(outputDir, "logs", filename)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		Setup(io.Discard, slog.LevelInfo)
		return func() {}
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		Setup(io.Discard, slog.LevelInfo)
		return func() {}
	}

	var w io.Writer = f
	if alsoStderr {
		w = io.MultiWriter(os.Stderr, f)
	}
	Setup(w, slog.LevelDebug)

	return func() { _ = f.Close() }
}
