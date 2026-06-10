package domain

import "strings"

// StateChange 角色/实体状态变化记录。
type StateChange struct {
	Chapter  int    `json:"chapter"`
	Entity   string `json:"entity"`              // 角色名或实体名
	Field    string `json:"field"`               // 变化属性：realm/location/status/power/relation 等
	OldValue string `json:"old_value,omitempty"` // 变化前（首次出现可空）
	NewValue string `json:"new_value"`           // 变化后
	Reason   string `json:"reason,omitempty"`    // 变化原因
}

// deadMarkers 判定死亡状态的标记词；deadExclusions 排除"假死/濒死"等非死亡语义。
// 机械匹配采用保守策略：排除词优先，宁可漏报不误报（漏报由 editor 七维评审兜底）。
var deadMarkers = []string{"死亡", "已死", "阵亡", "身亡", "殒命", "战死", "病逝", "坠亡", "处决", "气绝", "陨落", "丧命", "遇害", "魂飞魄散", "毙命"}
var deadExclusions = []string{"假死", "濒死", "诈死", "未死", "不死", "垂死", "半死", "九死", "求死", "找死"}

// IsDeadValue 机械判断 status 值是否表示死亡。
func IsDeadValue(v string) bool {
	for _, ex := range deadExclusions {
		if strings.Contains(v, ex) {
			return false
		}
	}
	for _, m := range deadMarkers {
		if strings.Contains(v, m) {
			return true
		}
	}
	return false
}

// FoldStateChanges 折叠状态变化：实体 → 属性 → 最新一条记录。
// 切片顺序即时间顺序（AppendStateChanges 仅追加），后者覆盖前者。
func FoldStateChanges(changes []StateChange) map[string]map[string]StateChange {
	out := make(map[string]map[string]StateChange)
	for _, c := range changes {
		if out[c.Entity] == nil {
			out[c.Entity] = make(map[string]StateChange)
		}
		out[c.Entity][c.Field] = c
	}
	return out
}

// DeadEntities 返回"最新 status 为死亡、且死亡记录早于 currentChapter"的实体 → 死亡记录章节。
// 同章死亡不算违规（角色可在本章出场后死亡）；后续复活（最新 status 非死亡）自动豁免。
func DeadEntities(changes []StateChange, currentChapter int) map[string]int {
	out := make(map[string]int)
	for entity, fields := range FoldStateChanges(changes) {
		c, ok := fields["status"]
		if !ok {
			continue
		}
		if IsDeadValue(c.NewValue) && c.Chapter < currentChapter {
			out[entity] = c.Chapter
		}
	}
	return out
}

// NormalizeDeadEntities 把 DeadEntities 结果的 key 经 别名→正名 表归一化（同正名取最早死亡章）。
// state_changes.entity 由 LLM 自由填写，可能记别名；出场名单也可能用别名——
// 两侧都折算到正名后比对，消除单向归一化的漏报缺口。
func NormalizeDeadEntities(dead map[string]int, aliasToCanonical map[string]string) map[string]int {
	out := make(map[string]int, len(dead))
	for entity, ch := range dead {
		canon := entity
		if c, ok := aliasToCanonical[entity]; ok {
			canon = c
		}
		// 同一正名命中多条（别名+正名分记）时取最早死亡章
		if prev, exists := out[canon]; !exists || ch < prev {
			out[canon] = ch
		}
	}
	return out
}
