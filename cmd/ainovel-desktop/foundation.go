package main

import (
	"fmt"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// ── 设定审阅 ──
//
// 引擎在逐章验收模式下会在"规划完成、正要写第 1 章"之前停下
// （internal/host/advance_gate.go 的 gate.Allow → StartsForwardChapter 无许可即暂停），
// 此时用户需要看到**完整**的设定来决定放行还是先改。
//
// UISnapshot 里的设定是给侧栏用的摘要（前提截断到 80 字、角色只有名字、世界观完全没有），
// 不足以审阅，所以这里直读 store 取全文。
//
// 读取方式：以书目录新建一个只读 Store。引擎的写入是 temp+fsync+rename 原子替换，
// 因此并发读只会读到某个完整版本，不会读到写坏的中间态。与 library.go 读进度同一手法。

// FoundationView 是供审阅的完整设定。
type FoundationView struct {
	Premise    string          `json:"premise"`
	Outline    []OutlineItem   `json:"outline"`
	Characters []CharacterItem `json:"characters"`
	WorldRules []WorldRuleItem `json:"worldRules"`
	Compass    *CompassItem    `json:"compass"`
	Volumes    []VolumeItem    `json:"volumes"`
	// AwaitingReview 为 true 表示引擎正停在"等待放行下一章"，此刻审阅最有意义。
	AwaitingReview bool `json:"awaitingReview"`
	NextChapter    int  `json:"nextChapter"`
}

type OutlineItem struct {
	Chapter   int      `json:"chapter"`
	Title     string   `json:"title"`
	CoreEvent string   `json:"coreEvent"`
	Hook      string   `json:"hook"`
	Scenes    []string `json:"scenes"`
}

type CharacterItem struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Role        string   `json:"role"`
	Description string   `json:"description"`
	Arc         string   `json:"arc"`
	Traits      []string `json:"traits"`
	Tier        string   `json:"tier"`
}

type WorldRuleItem struct {
	Category string `json:"category"`
	Rule     string `json:"rule"`
	Boundary string `json:"boundary"`
}

type CompassItem struct {
	EndingDirection string   `json:"endingDirection"`
	OpenThreads     []string `json:"openThreads"`
	EstimatedScale  string   `json:"estimatedScale"`
}

type VolumeItem struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Theme string `json:"theme"`
}

// GetFoundation 读取当前书的完整设定供审阅。
func (a *App) GetFoundation() (FoundationView, error) {
	h, err := a.reqHost()
	if err != nil {
		return FoundationView{}, err
	}
	st := storepkg.NewStore(h.Dir())
	var out FoundationView

	// 单项读取失败不应让整个面板打不开：设定是分批落盘的，
	// 规划中途查看时后面几项本来就还不存在。
	if premise, err := st.Outline.LoadPremise(); err == nil {
		out.Premise = premise
	}
	if entries, err := st.Outline.LoadOutline(); err == nil {
		for _, e := range entries {
			out.Outline = append(out.Outline, OutlineItem{
				Chapter: e.Chapter, Title: e.Title, CoreEvent: e.CoreEvent,
				Hook: e.Hook, Scenes: e.Scenes,
			})
		}
	}
	if chars, err := st.Characters.Load(); err == nil {
		for _, c := range chars {
			out.Characters = append(out.Characters, CharacterItem{
				Name: c.Name, Aliases: c.Aliases, Role: c.Role,
				Description: c.Description, Arc: c.Arc, Traits: c.Traits, Tier: c.Tier,
			})
		}
	}
	if rules, err := st.World.LoadWorldRules(); err == nil {
		for _, r := range rules {
			out.WorldRules = append(out.WorldRules, WorldRuleItem{
				Category: r.Category, Rule: r.Rule, Boundary: r.Boundary,
			})
		}
	}
	if compass, err := st.Outline.LoadCompass(); err == nil && compass != nil {
		out.Compass = &CompassItem{
			EndingDirection: compass.EndingDirection,
			OpenThreads:     compass.OpenThreads,
			EstimatedScale:  compass.EstimatedScale,
		}
	}
	if volumes, err := st.Outline.LoadLayeredOutline(); err == nil {
		for _, v := range volumes {
			out.Volumes = append(out.Volumes, VolumeItem{
				Index: v.Index, Title: v.Title, Theme: v.Theme,
			})
		}
	}

	// 是否正停在验收点：逐章验收模式 + 引擎未运行 + 已进入写作期。
	snap := h.Snapshot()
	out.AwaitingReview = snap.AdvanceMode == "review" && !snap.IsRunning &&
		snap.Phase == "writing" && snap.AdvancePermitChapter == 0
	if progress, err := st.Progress.Load(); err == nil && progress != nil {
		out.NextChapter = progress.NextChapter()
	}
	return out, nil
}

// ReviseFoundation 提交对设定的修改意见，交由 Arbiter 裁定后派 architect 修改。
// 这条路径复用引擎既有的用户干预通道（不直接改文件——大纲与进度/伏笔台账有关联，
// 绕过引擎手改会破坏事实层一致性）。
func (a *App) ReviseFoundation(instruction string) error {
	h, err := a.reqHost()
	if err != nil {
		return err
	}
	if instruction == "" {
		return fmt.Errorf("请填写修改意见")
	}
	// 引擎停机时 Continue 会裁定并按需拉起引擎；运行中则用 Steer 即时注入。
	if h.Snapshot().IsRunning {
		return h.Steer(instruction)
	}
	return h.Continue(instruction)
}
