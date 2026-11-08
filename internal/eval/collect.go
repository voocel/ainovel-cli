package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

// Collected 是一次运行产出的只读采集结果。全部来自已有评测器与事实层，eval 不自己重算。
type Collected struct {
	Dir         string
	Report      diag.Report // diag.Diagnose：工件 + 运行时 Findings + Stats
	Progress    *domain.Progress
	Checkpoints []domain.Checkpoint
	Pending     map[string]bool // 残留信号：pending_commit/pending_steer/last_commit/last_review
	LoadErrors  []string        // 契约依赖工件的真实读取失败（非"不存在"）；Grade 据此 hard fail
	RuntimeErr  string          // runner 捕获的运行时错误（hard fail），空=无
	Style       StyleCollection
	Usage       UsageMetrics
	ToolCalls   int
}

// StyleCollection 是从章节终稿中采集的全书文体事实。
type StyleCollection struct {
	Status string           `json:"status"` // ok / insufficient_sample
	Stats  *stylestat.Stats `json:"stats,omitempty"`
}

// UsageMetrics 是 meta/usage.json 中已有的可靠成本/token 事实。
type UsageMetrics struct {
	Input         int     `json:"input,omitempty"`
	Output        int     `json:"output,omitempty"`
	CacheRead     int     `json:"cache_read,omitempty"`
	CacheWrite    int     `json:"cache_write,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
	MissingUsage  int     `json:"missing_usage,omitempty"`
	UsageRecorded bool    `json:"usage_recorded"`
}

// Collect 对一个已完成的输出目录做离线采集。runtimeErr 是 runner 驱动期间的错误（如有）。
// 工件读取错误不静默吞：文件不存在视为"无数据"，其余（损坏/无权限）记入 LoadErrors，
// 避免"读不到 pending 文件"被误判成"没有 pending"而 false pass（fail-loud）。
func Collect(dir string, runtimeErr error) Collected {
	s := store.NewStore(dir)
	rep, _ := diag.Diagnose(s)

	var loadErrors []string
	check := func(name string, err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			loadErrors = append(loadErrors, fmt.Sprintf("%s: %v", name, err))
		}
	}

	prog, err := s.Progress.Load()
	check("progress", err)
	cks := s.Checkpoints.All()

	pending := map[string]bool{}
	pc, err := s.Signals.LoadPendingCommit()
	check("pending_commit", err)
	if pc != nil {
		pending["pending_commit"] = true
	}
	lc, err := s.Signals.LoadLastCommit()
	check("last_commit", err)
	if lc != nil {
		pending["last_commit"] = true
	}
	lr, err := s.Signals.LoadLastReviewSignal()
	check("last_review", err)
	if lr != nil {
		pending["last_review"] = true
	}
	rm, err := s.RunMeta.Load()
	check("run_meta", err)
	if rm != nil && rm.PendingSteer != "" {
		pending["pending_steer"] = true
	}
	style := collectStyle(s, prog, check)
	usage := collectUsage(s, check)
	toolCalls := countToolCalls(dir, check)

	errStr := ""
	if runtimeErr != nil {
		errStr = runtimeErr.Error()
	}
	return Collected{
		Dir:         dir,
		Report:      rep,
		Progress:    prog,
		Checkpoints: cks,
		Pending:     pending,
		LoadErrors:  loadErrors,
		RuntimeErr:  errStr,
		Style:       style,
		Usage:       usage,
		ToolCalls:   toolCalls,
	}
}

func collectStyle(s *store.Store, prog *domain.Progress, check func(string, error)) StyleCollection {
	input := stylestat.Input{}
	if prog != nil {
		chapters := append([]int(nil), prog.CompletedChapters...)
		sort.Ints(chapters)
		for _, ch := range chapters {
			text, err := s.Drafts.LoadChapterText(ch)
			check(fmt.Sprintf("chapter:%d", ch), err)
			if strings.TrimSpace(text) == "" {
				check(fmt.Sprintf("chapter:%d", ch), fmt.Errorf("progress 标记已完成但终稿为空"))
				continue
			}
			input.Chapters = append(input.Chapters, text)
			input.Titles = append(input.Titles, chapterTitle(s, ch, text, check))
		}
	}
	chars, err := s.Characters.Load()
	check("characters", err)
	for _, c := range chars {
		if c.Name != "" {
			input.Stopwords = append(input.Stopwords, c.Name)
		}
		input.Stopwords = append(input.Stopwords, c.Aliases...)
	}

	stats := stylestat.Compute(input)
	if stats == nil {
		return StyleCollection{Status: "insufficient_sample"}
	}
	return StyleCollection{Status: "ok", Stats: stats}
}

func chapterTitle(s *store.Store, chapter int, text string, check func(string, error)) string {
	summary, err := s.Summaries.LoadSummary(chapter)
	check(fmt.Sprintf("summary:%d", chapter), err)
	summaryTitle := ""
	if summary != nil {
		summaryTitle = summary.Title
	}
	return stylestat.TitleFromChapter(text, summaryTitle)
}

func collectUsage(s *store.Store, check func(string, error)) UsageMetrics {
	state, err := s.Usage.Load()
	check("usage", err)
	if state == nil {
		return UsageMetrics{}
	}
	return UsageMetrics{
		Input:         state.Overall.Input,
		Output:        state.Overall.Output,
		CacheRead:     state.Overall.CacheRead,
		CacheWrite:    state.Overall.CacheWrite,
		CostUSD:       state.Overall.Cost,
		MissingUsage:  state.MissingUsage,
		UsageRecorded: true,
	}
}

type sessionLine struct {
	agentcore.Message
}

func countToolCalls(dir string, check func(string, error)) int {
	sessionDir := filepath.Join(dir, "meta", "sessions")
	var total int
	err := filepath.WalkDir(sessionDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		n, err := countToolCallsInFile(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		check("sessions", err)
	}
	return total
}

func countToolCallsInFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var total int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var sl sessionLine
		if err := json.Unmarshal(sc.Bytes(), &sl); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		total += len(sl.Message.ToolCalls())
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

// HasCheckpoint 判断采集到的 checkpoint 中是否存在匹配 spec 的记录。
// spec 形如 "chapter:1:commit" / "arc:1:1:arc_summary" / "volume:1:volume_summary" / "global:layered_outline"。
func (c Collected) HasCheckpoint(spec string) (bool, error) {
	scope, step, err := parseCheckpointSpec(spec)
	if err != nil {
		return false, err
	}
	for _, ck := range c.Checkpoints {
		if ck.Step == step && ck.Scope == scope {
			return true, nil
		}
	}
	return false, nil
}

// parseCheckpointSpec 把契约串解析成 (Scope, step)。
func parseCheckpointSpec(spec string) (domain.Scope, string, error) {
	parts := strings.Split(spec, ":")
	bad := func() (domain.Scope, string, error) {
		return domain.Scope{}, "", fmt.Errorf("非法 checkpoint 契约: %q", spec)
	}
	if len(parts) < 2 {
		return bad()
	}
	kind := parts[0]
	switch domain.ScopeKind(kind) {
	case domain.ScopeChapter: // chapter:N:step
		if len(parts) != 3 {
			return bad()
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeChapter, Chapter: n}, parts[2], nil
	case domain.ScopeArc: // arc:V:A:step
		if len(parts) != 4 {
			return bad()
		}
		v, err1 := strconv.Atoi(parts[1])
		a, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeArc, Volume: v, Arc: a}, parts[3], nil
	case domain.ScopeVolume: // volume:V:step
		if len(parts) != 3 {
			return bad()
		}
		v, err := strconv.Atoi(parts[1])
		if err != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeVolume, Volume: v}, parts[2], nil
	case domain.ScopeGlobal: // global:step
		if len(parts) != 2 {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeGlobal}, parts[1], nil
	default:
		return bad()
	}
}
