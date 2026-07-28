package tui

import (
	"reflect"
	"strings"
	"testing"
)

// 章节参数解析失败必须报错而不是静默忽略：把 "3-5" 悄悄当成"未指定范围"会让技能
// 作用到用户没打算改的章节上。
func TestParseChapterArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    []int
		wantErr bool
	}{
		{"无参数", nil, nil, false},
		{"单章", []string{"3"}, []int{3}, false},
		{"区间", []string{"3-5"}, []int{3, 4, 5}, false},
		{"逗号列举", []string{"3,5,7"}, []int{3, 5, 7}, false},
		{"空格分隔", []string{"3", "5"}, []int{3, 5}, false},
		{"混合", []string{"1-2,5"}, []int{1, 2, 5}, false},
		// 区间两侧的空格容忍掉（前端与手输都可能带）；命令行走 Fields 时本就切开了。
		{"带空格的区间", []string{"3 - 5"}, []int{3, 4, 5}, false},
		{"非数字", []string{"abc"}, nil, true},
		{"零章", []string{"0"}, nil, true},
		{"负数", []string{"-1"}, nil, true},
		{"倒序区间", []string{"5-3"}, nil, true},
		{"零起点区间", []string{"0-3"}, nil, true},
		{"半个区间", []string{"3-"}, nil, true},
		{"空片段被跳过", []string{"3,,5"}, []int{3, 5}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChapterArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v (值 %v)", tc.wantErr, err, got)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseChapterArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// /skill 必须在注册表里，且不得设 NeedsIdle——RunSkill 运行中走 Steer、停机走
// Continue，两种状态都可用；设了 NeedsIdle 会让运行中的即时干预无从下手。
func TestSkillCommandRegistered(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("skill")
	if !ok {
		t.Fatal("/skill 应在命令注册表中")
	}
	if spec.NeedsIdle {
		t.Fatal("/skill 不应要求空闲：运行中经 Steer 注入同样有效")
	}
	if spec.Run == nil {
		t.Fatal("/skill 缺少执行函数")
	}
	if spec.Hidden {
		t.Fatal("/skill 应出现在帮助清单里")
	}
}

// reload 是保留子命令，必须在派单之前被截住：若它落进技能名分支，
// 用户会收到"未知技能 reload"，而真正想做的重扫目录没发生。
// 用零值 Model（runtime 为 nil）反证这一点——一旦走进任何 Host 调用就会 panic。
func TestSkillReloadRejectsExtraArgs(t *testing.T) {
	m := Model{}
	got, _ := runSkillCommand(m, []string{"reload", "3-5"})
	next, ok := got.(Model)
	if !ok {
		t.Fatalf("应返回 Model，得 %T", got)
	}
	if len(next.events) != 1 {
		t.Fatalf("应产生一条提示事件，实际 %d 条", len(next.events))
	}
	ev := next.events[0]
	if ev.Category != "ERROR" {
		t.Fatalf("参数用错应走错误事件，实际 %q", ev.Category)
	}
	if !strings.Contains(ev.Summary, "/skill reload") {
		t.Fatalf("提示应给出正确用法，实际 %q", ev.Summary)
	}
}

// 帮助文本必须写明 reload：技能装好后不重启就生效这条路径，用户只可能从这里发现。
func TestSkillCommandUsageMentionsReload(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("skill")
	if !ok {
		t.Fatal("/skill 应在命令注册表中")
	}
	if !strings.Contains(spec.Usage, "reload") {
		t.Fatalf("/skill 的用法应含 reload，实际 %q", spec.Usage)
	}
}

// scopeLabel 对未知取值原样回显，不伪装成已知范围——用户自定义技能写错 scope 时
// 应该看到原文，而不是被静默归类。
func TestScopeLabel(t *testing.T) {
	for scope, want := range map[string]string{
		"chapters":   "作用于已完成章节",
		"forward":    "作用于后续写作",
		"foundation": "作用于设定层",
		"whatever":   "whatever",
	} {
		if got := scopeLabel(scope); got != want {
			t.Fatalf("scopeLabel(%q) = %q, want %q", scope, got, want)
		}
	}
}
