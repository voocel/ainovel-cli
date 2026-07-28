package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/skills"
)

// buildSkillIntervention 是下游 Worker 看到的授权段原文，范围必须写死：
// 说"第 3-5 章"就只授权这三章，不留可被扩大解读的余地。
func TestBuildSkillInterventionPinsScope(t *testing.T) {
	sk := skills.Skill{
		Name: "anti-ai-tone", Description: "清理 AI 腔",
		Agent: "editor", Scope: skills.ScopeChapters, Body: "判据",
	}

	cases := []struct {
		name     string
		chapters []int
		want     []string
		reject   []string
	}{
		{
			name: "连续区间渲染成范围", chapters: []int{3, 4, 5},
			want:   []string{"第 3-5 章", "不得改动范围外的任何章节", "anti-ai-tone", "清理 AI 腔"},
			reject: []string{"最近已完成的章节"},
		},
		{
			name: "离散章节逐个列举", chapters: []int{3, 5, 7},
			want: []string{"第 3、5、7 章", "不得改动范围外的任何章节"},
		},
		{
			name: "单章", chapters: []int{2},
			want: []string{"第 2 章"},
		},
		{
			// 未指定范围时只能说"最近已完成的章节"，绝不能落一个具体区间——
			// 具体范围由 Engine 在执行期确定性推导，这里凭空写一个会与它冲突。
			name: "未指定范围", chapters: nil,
			want:   []string{"最近已完成的章节"},
			reject: []string{"不得改动范围外", "第 1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSkillIntervention(sk, tc.chapters)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("干预原文缺少 %q: %s", w, got)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(got, r) {
					t.Fatalf("干预原文不应含 %q: %s", r, got)
				}
			}
		})
	}

	// forward / foundation 技能不带章节，范围说明取自 scope。
	fwd := skills.Skill{Name: "voice", Description: "调笔法", Agent: "writer", Scope: skills.ScopeForward, Body: "x"}
	if got := buildSkillIntervention(fwd, nil); !strings.Contains(got, "后续写作") {
		t.Fatalf("forward 技能应声明作用于后续写作: %s", got)
	}
	found := skills.Skill{Name: "arc", Description: "调设定", Agent: "architect_long", Scope: skills.ScopeFoundation, Body: "x"}
	if got := buildSkillIntervention(found, nil); !strings.Contains(got, "设定层") {
		t.Fatalf("foundation 技能应声明作用于设定层: %s", got)
	}
}

// 章节参数来自手输与前端两处，脏数据必须在合成干预前规整掉。
func TestNormalizeChapters(t *testing.T) {
	cases := []struct {
		in   []int
		want []int
	}{
		{nil, nil},
		{[]int{3, 1, 2}, []int{1, 2, 3}},
		{[]int{2, 2, 2}, []int{2}},
		{[]int{0, -1, 3}, []int{3}},
		{[]int{0}, []int{}},
	}
	for _, tc := range cases {
		got := normalizeChapters(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("normalizeChapters(%v) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("normalizeChapters(%v) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// RunSkill 的机械校验必须在裁定之前挡住明显错误：让 Arbiter 去拒绝会白花一次模型
// 调用，且错误会被包装成"裁定失败"，不如直接给用户可操作的提示。
func TestRunSkillRejectsBadInputBeforeArbitration(t *testing.T) {
	h := &Host{skills: skills.Load(skills.LoadOptions{})}

	if err := h.RunSkill("   ", nil); err == nil {
		t.Fatal("空技能名应被拒")
	}
	err := h.RunSkill("no-such-skill", nil)
	if err == nil {
		t.Fatal("未知技能应被拒")
	}
	// 错误必须列出可用技能，否则用户只知道错了、不知道能用什么。
	if !strings.Contains(err.Error(), "anti-ai-tone") {
		t.Fatalf("未知技能的错误应列出可用清单: %v", err)
	}

	// scope 不接受章节号的技能带了章节 → 拒（范围语义矛盾）。
	catalog := h.skills
	var forwardName string
	for _, sk := range catalog.List() {
		if sk.Scope != skills.ScopeChapters {
			forwardName = sk.Name
			break
		}
	}
	if forwardName != "" {
		if err := h.RunSkill(forwardName, []int{3}); err == nil {
			t.Fatalf("技能 %s 的范围不接受章节号，应被拒", forwardName)
		}
	}
	if err := h.RunSkill("anti-ai-tone", []int{0}); err == nil {
		t.Fatal("显式传入非正章节号应被拒绝，不能静默退化成最近章节")
	}
}

// 重载必须在引擎运行时被拒：裁定时看到的名单与执行时取到的正文必须是同一份快照，
// 运行中换目录会让一次已裁定的技能派单在执行期取到不同正文（甚至取不到）。
func TestReloadSkillsRefusedWhileRunning(t *testing.T) {
	h := &Host{
		lifecycle: lifecycleRunning,
		skills:    skills.Load(skills.LoadOptions{}),
	}
	before := h.skillCatalog().Len()

	if _, _, err := h.ReloadSkills(); err == nil {
		t.Fatal("引擎运行中应拒绝重载技能")
	}
	if got := h.skillCatalog().Len(); got != before {
		t.Fatalf("被拒的重载不得改动现有目录：%d → %d", before, got)
	}
}

// 重载要真的换掉 engine 手里的那份副本：执行期取正文用的是 engine.skills，
// 只换 Host 的会让新技能"裁定名单里有、执行时找不到正文"。
func TestReloadSkillsReplacesEngineCatalog(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	h := &Host{
		lifecycle: lifecycleIdle,
		skills:    skills.Load(skills.LoadOptions{}),
		engine:    &engine{},
	}
	if _, ok := h.skillCatalog().Get("scratch-skill"); ok {
		t.Fatal("测试前提不成立：scratch-skill 不该预先存在")
	}

	// 落一个本书层技能，模拟用户"装一个技能"。DefaultOptions 的项目层绑定 cwd。
	projectDir := skills.DefaultProjectSkillsDir(dir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "这里是新技能的判据正文。"
	content := "---\nname: scratch-skill\ndescription: 临时技能\nagent: editor\nscope: chapters\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, "scratch-skill.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	n, problems, err := h.ReloadSkills()
	if err != nil {
		t.Fatalf("停机时重载应成功: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("合法技能不应报问题：%+v", problems)
	}
	if n != h.skillCatalog().Len() {
		t.Fatalf("返回的技能数应与目录一致：%d vs %d", n, h.skillCatalog().Len())
	}
	if _, ok := h.skillCatalog().Get("scratch-skill"); !ok {
		t.Fatal("重载后新技能应可见，否则『放好文件点重新扫描』这条路径不成立")
	}
	sk, ok := h.engine.skills.Get("scratch-skill")
	if !ok {
		t.Fatal("engine 的副本也必须换掉，否则执行期取不到正文")
	}
	if !strings.Contains(sk.Body, body) {
		t.Fatalf("engine 取到的正文不对: %q", sk.Body)
	}
}

// Skills 投影必须字段齐全：UI 靠 description 让用户判断该不该用，靠 source
// 看清哪一层生效，靠 body 看清 Worker 会收到什么判据。
func TestHostSkillsView(t *testing.T) {
	h := &Host{skills: skills.Load(skills.LoadOptions{})}
	list := h.Skills()
	if len(list) == 0 {
		t.Fatal("内置技能应可见")
	}
	for _, sk := range list {
		if sk.Name == "" || sk.Description == "" || sk.Agent == "" || sk.Scope == "" ||
			sk.Source == "" || sk.Body == "" {
			t.Fatalf("技能投影字段不全: %+v", sk)
		}
		if !strings.HasPrefix(sk.Source, "builtin:") {
			t.Fatalf("仅内置层时来源标签应为 builtin:*，实际 %q", sk.Source)
		}
	}
}
