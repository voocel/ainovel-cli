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
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 时间只保留时分秒，节省空间
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})
	slog.SetDefault(slog.New(h))
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
