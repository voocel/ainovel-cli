package eval

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/logger"
)

// RunOptions 控制单次 case 运行。
type RunOptions struct {
	OutputDir string        // 隔离输出目录（必填）
	Timeout   time.Duration // 单 case 墙钟上限；0 表示不限
	Progress  io.Writer     // 进度行输出（可选，nil 则不打印）
}

// RunCase 驱动一次 case：装配 host → 启动 → 按章数上限推进 → 到点 Abort。
// bundle 由调用方做过 variant 覆盖（如有）。返回的 error 即"运行时错误"（hard fail 依据）；
// 正常写完或正常截停都返回 nil。不设 ask_user handler——无人值守下该工具自动返回非阻塞提示。
//
// RunCase 独占并重置 OutputDir：StartPrepared 只重置 progress/checkpoints，不清 chapters/
// foundation 等工件，复用旧目录会让残留产物污染 diag 与 novel_context。故运行前清空，保证隔离。
func RunCase(cfg bootstrap.Config, bundle assets.Bundle, c Case, opts RunOptions) error {
	if strings.TrimSpace(opts.OutputDir) == "" {
		return fmt.Errorf("RunCase: 缺少 OutputDir")
	}
	if err := os.RemoveAll(opts.OutputDir); err != nil {
		return fmt.Errorf("清理输出目录: %w", err)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录: %w", err)
	}
	cfg.OutputDir = opts.OutputDir
	if c.Style != "" {
		cfg.Style = c.Style
	}

	eng, err := host.New(cfg, bundle)
	if err != nil {
		return fmt.Errorf("装配 host: %w", err)
	}
	// 落 logs/headless.log，diag 的运行时规则（stream idle storm 等）从中取证；
	// 会话 jsonl 由引擎自写，无需额外接线。defer 顺序对齐 headless：Close 先于 cleanup
	// 执行，收尾日志仍被文件捕获。
	cleanup := logger.SetupFile(eng.Dir(), "headless.log", false)
	defer cleanup()
	defer eng.Close()

	plan, err := startup.PrepareQuick(startup.Request{
		Mode:       startup.ModeQuick,
		UserPrompt: c.Prompt,
		OutputDir:  eng.Dir(),
	})
	if err != nil {
		return err
	}
	if err := eng.PrepareUserRules(plan.RawPrompt); err != nil {
		return fmt.Errorf("准备用户规则: %w", err)
	}
	if err := eng.StartPrepared(plan.StartPrompt); err != nil {
		return fmt.Errorf("启动: %w", err)
	}

	return drive(eng, c.MaxChapters, opts)
}

// driveEngine 是 drive 消费的最小引擎接口（*host.Host 天然满足）。抽出来是为了给
// drain-to-Done 纪律写确定性测试——这段并发逻辑出过 send-on-closed-channel 的坑。
type driveEngine interface {
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Snapshot() host.UISnapshot
	Abort() bool
}

// drive 消费引擎事件流，到章数上限或超时即 Abort，等 Done 收场。
//
// 关键纪律：无论正常完成、章数截停还是超时，都必须 drain 到 Done 才返回。host 后台 waitDone
// 会向 done 发送一次，而 eng.Close()（RunCase 的 defer）会 close(done)——提前返回触发 Close
// 会与 waitDone 的发送竞争关闭通道而 panic（send on closed channel）。headless 同样靠"先 Done
// 后 Close"。同时必须排空 Events 与 Stream，避免阻塞引擎。
func drive(eng driveEngine, maxChapters int, opts RunOptions) error {
	var timeoutCh <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	aborted, timedOut := false, false
	// finish 在 drain 到 Done（或通道关闭）后调用：超时则返回 error，否则正常结束。
	finish := func() error {
		if timedOut {
			return fmt.Errorf("运行超时（%s）", opts.Timeout)
		}
		return nil
	}
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return finish()
			}
			if opts.Progress != nil && strings.TrimSpace(ev.Summary) != "" {
				fmt.Fprintf(opts.Progress, "    [%s] %s\n", ev.Category, ev.Summary)
			}
			if !aborted && capReached(eng.Snapshot(), maxChapters) {
				eng.Abort()
				aborted = true
				timeoutCh = nil // 已达截停条件，转入正常收尾，不再受超时约束（避免把成功截停误判为超时）
			}
		case <-eng.Stream():
			// 排空流式增量，不消费内容——eval 不关心正文流，只看落盘事实。
		case _, ok := <-eng.Done():
			if !ok {
				return finish()
			}
			return finish()
		case <-timeoutCh:
			eng.Abort() // 此处 aborted 必为 false（cap 截停会把 timeoutCh 置 nil）
			aborted, timedOut = true, true
			timeoutCh = nil // 禁用计时器，继续 drain 直至 Done，再由 finish 返回超时错误
		}
	}
}

// capReached 判断是否达到截停条件。maxChapters>0 按已完成章数；<=0 视为"规划类"，
// 规划完成（进入 writing 或已 complete）即停。
func capReached(snap host.UISnapshot, maxChapters int) bool {
	if maxChapters <= 0 {
		return snap.Phase == string(domain.PhaseWriting) || snap.Phase == string(domain.PhaseComplete)
	}
	return snap.CompletedCount >= maxChapters
}
