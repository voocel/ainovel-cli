package novel

import (
	"fmt"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Length 是作品的篇幅口径（D63）：用户固定的章数、AI 的故事罗盘，以及由二者推出的
// 全书章数。推导、续跑与工作台共用这一条规则。
type Length struct {
	Fixed   int            `json:"fixed,omitempty"`   // 用户固定的全书章数；0 表示交给 AI
	Compass *model.Compass `json:"compass,omitempty"` // AI 给出的终局、收官承诺与篇幅上限
	// Final 是全书章数：固定时取 Fixed，否则取仍有效的收官承诺；0 表示尚未确定（开放期）。
	Final int `json:"final,omitempty"`
}

// LengthOf 按最近一轮创作的篇幅设定推出作品篇幅；没有运行时篇幅交给 AI。
func LengthOf(project projectdoc.Snapshot, run *model.CreationRun) Length {
	fixed := 0
	if run != nil {
		if goal, err := model.DecodeNovelGoal(run.Goal); err == nil {
			fixed = goal.TargetChapters
		}
	}
	return lengthOf(project, fixed)
}

func lengthOf(project projectdoc.Snapshot, fixed int) Length {
	length := Length{Fixed: fixed, Compass: project.Compass, Final: fixed}
	if fixed == 0 {
		length.Final = CommittedFinal(project.Compass, len(model.ChapterPlansInOrder(project.Plan)))
	}
	return length
}

// CommittedFinal 是 AI 收官承诺的生效值：不少于已规划章数才有效，更小视为过期（蓝图已被
// 改动越过它），按尚未收官处理，返回 0。推导器与作品库共用这一条规则。
func CommittedFinal(compass *model.Compass, planned int) int {
	if compass == nil || compass.Final < planned {
		return 0
	}
	return compass.Final
}

// planningGoal 按篇幅阶段说明规划要对罗盘做什么。终局方向总由规划写进罗盘（D70）。
// 篇幅交给 AI 时不预计、不给数字（D96）：何时收官由故事决定，篇幅上限只在蓝图触及它时
// 由提交边界说明。
func (l Length) planningGoal() string {
	switch {
	case l.Fixed > 0 && l.Compass == nil:
		return fmt.Sprintf("全书固定 %d 章：本次必须给出故事罗盘 compass，只写终局方向 ending（篇幅已由用户固定，不写 scale_max 与 final）；规划到第 %d 章时收束主线，%s", l.Fixed, l.Fixed, closing)
	case l.Fixed > 0:
		return fmt.Sprintf("全书固定 %d 章：规划到第 %d 章时收束主线，%s；终局方向有变化时修订 compass.ending", l.Fixed, l.Fixed, closing)
	case l.Compass == nil:
		return "篇幅由你决定：本次必须给出故事罗盘 compass，写下终局方向 ending；规划已到故事终点时同时声明收官 final"
	case l.Compass.Final > 0 && l.Final == 0:
		return fmt.Sprintf("篇幅由你决定：罗盘的收官承诺 %d 章已少于蓝图章数而失效，本次必须修订 compass.final（不少于已规划章数）或撤回收官（省略 final）", l.Compass.Final)
	case l.Final > 0:
		return fmt.Sprintf("已承诺全书 %d 章收官：规划到第 %d 章时收束主线，%s；确需调整就修订 compass.final", l.Final, l.Final, closing)
	default:
		return "篇幅由你决定：故事已到收束点时声明收官 final（全书章数，不少于已规划章数），并" + closing
	}
}

// closing 是收官时对蓝图的交代：长线有答案，伏笔回收，写不到的卷与故事弧删去（D97）。
const closing = "给每条未答的长线定下作答的章，把未回收的伏笔分配进收官章节，删去写不到的卷与故事弧"

// inputID 是规划任务 ID 中标识篇幅输入的一段（D67）：固定章数、篇幅上限与收官承诺
// 原值决定目标文案与提交边界，任一变化即是新任务。
func (l Length) inputID() string {
	scale, final := 0, 0
	if l.Compass != nil {
		scale, final = l.Compass.ScaleMax, l.Compass.Final
	}
	return fmt.Sprintf("f%d:s%d:e%d", l.Fixed, scale, final)
}
