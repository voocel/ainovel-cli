package diag

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
	"github.com/voocel/agentcore"
)

const (
	logTailCap   = 200 << 10 // 日志只取尾部 200KB（循环是近端现象）
	sessionTail  = 80        // 骨架尾巴条数（看派发先后顺序）
	repeatWindow = 150       // 重复聚合只看近端这么多条事件——长跑里正常工具累计上百次，
	// 真循环是近端高度集中；用窗口而非累计，避免把"正常推进"误判成"死循环"。
	recentAgents = 2  // 额外扫描最近活跃的子代理会话数
	repeatMin    = 3  // 重复达到几次才算"高频信号"
	repeatTopN   = 12 // 重复签名最多列几条
)

// RuntimeCapture 是一次运行时抓取的脱敏结果。只承载运行时信号；
// phase/flow/章节等创作态由 Report.Stats 携带，不在此重复。
type RuntimeCapture struct {
	GoOS, GoArch  string
	Models        []RoleModel  // 各会话实际生效的 provider/model（从 _meta 收集）
	CurrentStep   string       // 最新 checkpoint：scope.step
	StuckStep     string       // 尾部连续同 step；"" = 不卡
	StuckCount    int          // 连续次数
	Repeats       []RepeatStat // 重复签名 top-N（循环信号）
	DupContent    []DupStat    // 同 sha 文本反复出现（反复生成同段）
	LogKinds      map[string]int
	LogErrors     int
	LogWarns      int
	StopGuard     int
	Tail          []SkelEvent // 末 N 条骨架（看顺序）
	RedactedTexts int         // 打码文本块总数（脱敏自检）
	Sources       []string    // 实际读到的源（自检）
}

// RoleModel 记录某会话实际用的 provider/model。
type RoleModel struct {
	Agent, Provider, Model string
}

// RepeatStat 是一条重复签名及其次数。
type RepeatStat struct {
	Sig   string
	Count int
}

// DupStat 是同一段脱敏文本反复出现的次数。
type DupStat struct {
	Sha   string
	Count int
}

// sessionLine 解析 sessions/*.jsonl 的一行：内嵌 agentcore.Message + 可选 _meta。
type sessionLine struct {
	agentcore.Message
	Meta *struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"_meta"`
}

var kindRe = regexp.MustCompile(`kind=(\S+)`)

// CaptureRuntime 从 output 目录只读抓取运行时信号并脱敏聚合。
// 任何源缺失都安全降级（不报错），尽力而为。
func CaptureRuntime(s *store.Store) RuntimeCapture {
	rc := RuntimeCapture{GoOS: runtime.GOOS, GoArch: runtime.GOARCH, LogKinds: map[string]int{}}

	rc.CurrentStep, rc.StuckStep, rc.StuckCount = analyzeCheckpoints(s.Checkpoints.All())
	captureSessions(s.Dir(), &rc)
	captureLog(s.Dir(), &rc)
	return rc
}

// analyzeCheckpoints 取最新 step，并算尾部连续同 step（卡住信号）。
func analyzeCheckpoints(cps []domain.Checkpoint) (current, stuck string, count int) {
	if len(cps) == 0 {
		return "", "", 0
	}
	key := func(c domain.Checkpoint) string { return fmt.Sprintf("%s.%s", c.Scope, c.Step) }
	current = key(cps[len(cps)-1])
	n := 1
	for i := len(cps) - 2; i >= 0; i-- {
		if key(cps[i]) == current {
			n++
		} else {
			break
		}
	}
	if n >= repeatMin {
		stuck, count = current, n
	}
	return current, stuck, count
}

// captureSessions 扫描 coordinator + 最近子代理会话，脱敏聚合。
func captureSessions(dir string, rc *RuntimeCapture) {
	sessDir := filepath.Join(dir, "meta", "sessions")
	files := sessionFiles(sessDir)

	repeats := map[string]int{}
	dups := map[string]int{}
	models := map[string]RoleModel{}

	for _, f := range files {
		evs := scanSession(filepath.Join(sessDir, f.path), f.agent, rc, models)
		// 聚合只看近端窗口：长跑里 subagent/novel_context 累计上百次是正常推进，
		// 不是循环；真死循环是近端高度集中。
		aggregateRepeats(f.agent, tailEvents(evs, repeatWindow), repeats, dups)
		// 骨架尾巴优先取 coordinator——派发循环在这看得最清。
		if f.agent == "coordinator" && len(evs) > 0 {
			rc.Tail = tailEvents(evs, sessionTail)
		}
		rc.Sources = append(rc.Sources, "sessions/"+f.path)
	}
	if len(rc.Tail) == 0 {
		// 无 coordinator 会话时退回最近一个子代理。
		for _, f := range files {
			if evs := scanSessionTailOnly(filepath.Join(sessDir, f.path), f.agent); len(evs) > 0 {
				rc.Tail = tailEvents(evs, sessionTail)
				break
			}
		}
	}

	rc.Repeats = topRepeats(repeats)
	rc.DupContent = topDups(dups)
	rc.Models = sortedModels(models)
}

type sessionFile struct {
	path  string // 相对 sessDir
	agent string
}

// sessionFiles 返回 coordinator.jsonl + 最近活跃的子代理会话。
func sessionFiles(sessDir string) []sessionFile {
	var out []sessionFile
	if _, err := os.Stat(filepath.Join(sessDir, "coordinator.jsonl")); err == nil {
		out = append(out, sessionFile{path: "coordinator.jsonl", agent: "coordinator"})
	}

	agentsDir := filepath.Join(sessDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return out
	}
	type withTime struct {
		name string
		mod  int64
	}
	var agents []withTime
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil {
			agents = append(agents, withTime{e.Name(), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].mod > agents[j].mod })
	for i, a := range agents {
		if i >= recentAgents {
			break
		}
		stem := strings.TrimSuffix(a.name, ".jsonl")
		out = append(out, sessionFile{path: filepath.Join("agents", a.name), agent: stem})
	}
	return out
}

// scanSession 读一个会话文件，逐行脱敏，收集事件序列与 per-agent 模型。
// 重复/同段聚合不在这里做——交给 aggregateRepeats 在近端窗口上算。
func scanSession(path, agent string, rc *RuntimeCapture, models map[string]RoleModel) []SkelEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var evs []SkelEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var sl sessionLine
		if json.Unmarshal(sc.Bytes(), &sl) != nil {
			continue
		}
		ev := redactMessage(agent, sl.Message)
		evs = append(evs, ev)
		rc.RedactedTexts += ev.Redacted
		if sl.Meta != nil && (sl.Meta.Provider != "" || sl.Meta.Model != "") {
			models[agent] = RoleModel{Agent: agent, Provider: sl.Meta.Provider, Model: sl.Meta.Model}
		}
	}
	return evs
}

// aggregateRepeats 在给定事件窗口上累计重复签名与同段文本。
func aggregateRepeats(agent string, evs []SkelEvent, repeats, dups map[string]int) {
	for _, ev := range evs {
		for _, t := range ev.Tools {
			sig := agent + " · " + t.Name
			if t.Invalid {
				sig += " (args invalid)"
			}
			repeats[sig]++
		}
		if ev.ErrClass != "" {
			repeats[agent+" · err: "+ev.ErrClass]++
		}
		if ev.TextSha != "" {
			dups[ev.TextSha]++
		}
	}
}

// scanSessionTailOnly 仅取骨架（不计聚合），用于 coordinator 缺失时的兜底尾巴。
func scanSessionTailOnly(path, agent string) []SkelEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var evs []SkelEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var sl sessionLine
		if json.Unmarshal(sc.Bytes(), &sl) != nil {
			continue
		}
		evs = append(evs, redactMessage(agent, sl.Message))
	}
	return evs
}

func tailEvents(evs []SkelEvent, n int) []SkelEvent {
	if len(evs) <= n {
		return evs
	}
	return evs[len(evs)-n:]
}

// captureLog 读日志尾部，只聚合结构信号（kind/error/warn/stop_guard），
// 不把原始日志行入包——Detail 可能夹带正文。
func captureLog(dir string, rc *RuntimeCapture) {
	path := filepath.Join(dir, "logs", "tui.log")
	tail, ok := readTail(path)
	if !ok {
		path = filepath.Join(dir, "logs", "headless.log")
		tail, ok = readTail(path)
	}
	if !ok {
		return
	}
	rc.Sources = append(rc.Sources, "logs/"+filepath.Base(path)+" (尾部)")

	sc := bufio.NewScanner(bytes.NewReader(tail))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "level=ERROR"):
			rc.LogErrors++
		case strings.Contains(line, "level=WARN"):
			rc.LogWarns++
		}
		if m := kindRe.FindStringSubmatch(line); m != nil {
			rc.LogKinds[m[1]]++
		}
		if strings.Contains(line, "stop_guard") {
			rc.StopGuard++
		}
	}
}

// readTail 读文件尾部 logTailCap 字节，并丢弃首个可能被截断的半行。
func readTail(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	size := info.Size()
	var off int64
	if size > logTailCap {
		off = size - logTailCap
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, true
}

func topRepeats(m map[string]int) []RepeatStat {
	var out []RepeatStat
	for sig, c := range m {
		if c >= repeatMin {
			out = append(out, RepeatStat{Sig: sig, Count: c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Sig < out[j].Sig
	})
	if len(out) > repeatTopN {
		out = out[:repeatTopN]
	}
	return out
}

func topDups(m map[string]int) []DupStat {
	var out []DupStat
	for sha, c := range m {
		if c >= repeatMin {
			out = append(out, DupStat{Sha: sha, Count: c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Sha < out[j].Sha
	})
	return out
}

func sortedModels(m map[string]RoleModel) []RoleModel {
	out := make([]RoleModel, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}
