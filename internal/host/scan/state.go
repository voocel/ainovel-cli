package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Action 是 NextAction 从扫榜库事实推导出的下一步确定性动作。
// 持久状态不写会漂移的阶段枚举；下一动作只由工件推导。
type Action string

const (
	ActionFetch   Action = "fetch"
	ActionParse   Action = "parse"
	ActionAnalyze Action = "analyze"
	ActionTopic   Action = "topic"
	ActionDone    Action = "done"
)

// Facts 是从扫榜库读出的、决定下一动作所需的最小事实快照。
// 把纯决策（NextAction）与 IO（LoadState）分离：NextAction 对同一 Facts 恒定。
// 全字段可比较（无切片）——runner 的停滞检测靠 facts == *previous。
//
// 没有独立的 Cleaned：清洗是纯 Go 变换，与解析同一步落成 entries.json，
// 单独立一个事实位会造出一个永远不可能为假的状态。
type Facts struct {
	LibraryReady bool // _meta.json + sources/ 快照齐备
	Parsed       bool // entries.json 新鲜（清洗后的条目，唯一数据真源）
	ValidEntries int
	Sparse       bool
	Analyzed     bool // analysis.json 新鲜
	Topiced      bool // topic.json 新鲜
}

// NextAction 沿固定线性管线，返回第一份缺失或未满足的动作。纯函数，无 IO。
func NextAction(f Facts) Action {
	switch {
	case f.Topiced:
		// 选题是终态：全部产物已落盘。上游工件因 prompt 版本升级失鲜不再要求重做——
		// 否则版本升级会把已扫完的榜追溯判回半路，用户每次升级都要重付一遍调用。
		return ActionDone
	case !f.LibraryReady:
		return ActionFetch
	case !f.Parsed:
		return ActionParse
	case !f.Analyzed:
		return ActionAnalyze
	default:
		return ActionTopic
	}
}

// LoadState 从扫榜库读取事实。逐级比对 InputDigest：上游变化自动失效下游。
func LoadState(l *Library, opts Options) (Facts, error) {
	var f Facts

	if !l.Active() {
		return f, nil
	}

	meta, err := l.LoadMeta()
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, fmt.Errorf("读取扫榜库元数据: %w", err)
	}

	// 数据源快照是库身份的另一半。本次若还提供了数据源，必须与快照一致；
	// runner 会先完成安全换入，直接调用 LoadState 时则显式报错，绝不静默复用旧报告。
	savedSources, err := l.LoadSources()
	if err != nil {
		if errors.Is(err, errSourcesUnavailable) {
			return f, nil
		}
		return f, fmt.Errorf("读取扫榜库数据源快照: %w", err)
	}
	savedDigest := sourcesDigest(savedSources)
	if meta.SourceDigest != "" && meta.SourceDigest != savedDigest {
		return f, fmt.Errorf("库内 sources/ 与 _meta.json 的数据源摘要不一致，请修复或重建 %s", l.Dir())
	}
	if provided, ok, err := sourcesFromOptions(context.Background(), opts); err != nil {
		return f, fmt.Errorf("读取本次数据源: %w", err)
	} else if ok && sourcesDigest(provided) != savedDigest {
		return f, fmt.Errorf("本次榜单数据与库内快照不同，必须先更新数据源，拒绝复用旧报告")
	}
	f.LibraryReady = true

	// entries.json：身份是「归一化条目 + 采集日期 + 平台」，无法从别处重算，
	// 因此它自报的 InputDigest 即是真值——解析阶段本身就是这份身份的产生处。
	entArt, err := readArtifact[cleanedEntries](l, fileEntries)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("读取条目工件: %w", err)
	}
	if entArt.Payload.SourceDigest != savedDigest {
		return f, nil
	}
	f.Parsed = true
	f.ValidEntries = entArt.Payload.Quality.ValidEntries
	f.Sparse = entArt.Payload.Quality.Sparse

	// 期望的条目身份能从载荷重算：与工件自报的 InputDigest 不一致说明文件被改过。
	want := entriesInputDigest(entArt.Payload.Entries, meta.ScanDate, meta.Platform)
	if entArt.InputDigest != want {
		return f, fmt.Errorf("%s 的输入摘要与载荷不一致（文件可能被手工改过）：请删除 %s 后重扫",
			fileEntries, l.Dir())
	}

	entRaw, err := l.readBytes(fileEntries)
	if err != nil {
		return f, fmt.Errorf("读取条目工件原始字节: %w", err)
	}

	f.Analyzed, err = artifactFresh[Analysis](l, fileAnalysis, analysisInputDigest(entRaw))
	if err != nil {
		return f, fmt.Errorf("检查趋势工件: %w", err)
	}
	if !f.Analyzed {
		return f, nil
	}

	anaRaw, err := l.readBytes(fileAnalysis)
	if err != nil {
		return f, fmt.Errorf("读取趋势工件原始字节: %w", err)
	}
	f.Topiced, err = artifactFresh[TopicReport](l, fileTopic, topicInputDigest(anaRaw))
	if err != nil {
		return f, fmt.Errorf("检查选题工件: %w", err)
	}

	return f, nil
}

// ResumeSummary 返回某个扫榜库未完成扫描的一行提示（无未完成扫描则空串）。
// 会重算各工件的 InputDigest，只适合按需调用，不要放进快照轮询。
func ResumeSummary(dir string) string {
	l := OpenLibrary(dir)
	if !l.Active() {
		return ""
	}
	f, err := LoadState(l, Options{})
	if err != nil {
		return fmt.Sprintf("扫榜库 %s 状态异常：%v", dir, err)
	}
	switch NextAction(f) {
	case ActionDone:
		return ""
	case ActionFetch:
		return fmt.Sprintf("扫榜库 %s 只有半份身份，需重新提供数据源", dir)
	case ActionParse:
		return fmt.Sprintf("扫榜库 %s 待解析条目", dir)
	case ActionAnalyze:
		return fmt.Sprintf("扫榜库 %s 已有 %d 条条目，待趋势分析", dir, f.ValidEntries)
	default:
		return fmt.Sprintf("扫榜库 %s 已完成趋势分析，待选题决策", dir)
	}
}
