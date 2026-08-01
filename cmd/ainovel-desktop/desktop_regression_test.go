package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/scan"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestBookOpenResult_CompleteBookHasProgress(t *testing.T) {
	got := bookOpenResultFromSnapshot(host.UISnapshot{Phase: string(domain.PhaseComplete)})
	if !got.HasProgress {
		t.Fatal("已完结书即使没有 RecoveryLabel 也必须判定为有进度")
	}
	if got.RecoveryLabel != "" {
		t.Fatalf("测试前提不成立：完结书恢复标签应为空，实际 %q", got.RecoveryLabel)
	}

	empty := bookOpenResultFromSnapshot(host.UISnapshot{})
	if empty.HasProgress {
		t.Fatal("空快照不应进入创作工作台")
	}
}

func TestGetConfigWithoutOpenBook(t *testing.T) {
	a := NewApp(versionForTest())
	a.cfgLoaded = true
	a.baseCfg = bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-test",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {APIKey: "1234567890abcdef", Models: []bootstrap.ModelConfig{{Name: "gpt-test"}}},
		},
	}

	view, err := a.GetConfig()
	if err != nil {
		t.Fatalf("未打开书时 GetConfig 不应失败: %v", err)
	}
	if view.DefaultProvider != "openai" || len(view.Providers) != 1 {
		t.Fatalf("配置投影不正确: %+v", view)
	}
	if view.Providers[0].APIKeyHint == "1234567890abcdef" {
		t.Fatal("配置投影泄露了完整 API Key")
	}
}

func TestJobRegistryActiveLifecycle(t *testing.T) {
	r := newJobRegistry()
	if r.active() {
		t.Fatal("新注册表不应报告后台作业")
	}
	ctx, _ := r.begin("import")
	if !r.active() {
		t.Fatal("导入开始后应报告后台作业")
	}
	r.abortAll()
	if r.active() {
		t.Fatal("全部取消后不应再报告后台作业")
	}
	if ctx.Err() == nil {
		t.Fatal("abortAll 应取消在途作业 context")
	}
}

func TestJobRegistryOldCompletionDoesNotForgetReplacement(t *testing.T) {
	r := newJobRegistry()
	first, firstID := r.begin("import")
	second, _ := r.begin("import")
	if first.Err() == nil {
		t.Fatal("同类作业重入后旧 context 应被取消")
	}

	// 模拟旧 goroutine 比新作业晚返回。它只能结束自己，不能删掉新作业句柄。
	r.end("import", firstID)
	if !r.active() {
		t.Fatal("旧作业的 defer 误删了新作业句柄")
	}
	r.abort("import")
	if second.Err() == nil {
		t.Fatal("替代作业仍应能被取消")
	}
}

func TestPrepareRankScanOptionsAllowsSnapshotOnlyResume(t *testing.T) {
	root := t.TempDir()
	path, err := scan.LibraryPath(root, "qidian", "月票榜", "20260101")
	if err != nil {
		t.Fatal(err)
	}
	l, err := scan.InitLibrary(path, "qidian", "月票榜", "20260101", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SaveSources([]scan.Source{{Platform: "qidian", RankName: "月票榜", Raw: "快照", Origin: "paste"}}); err != nil {
		t.Fatal(err)
	}
	got, err := prepareRankScanOptions(RankScanOptions{
		Platform: "qidian", RankName: "月票榜", LibraryDir: root, ScanDate: "20260101",
	})
	if err != nil {
		t.Fatalf("仅凭快照恢复不应被桌面入口拒绝: %v", err)
	}
	if got.PastedText != "" || got.FilePath != "" || got.DirPath != "" {
		t.Fatalf("恢复不应伪造数据源: %+v", got)
	}
}

func TestEnsureSimulationSourceDirCreatesDirectory(t *testing.T) {
	bookDir := t.TempDir()
	got, err := ensureSimulationSourceDir(bookDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bookDir, "simulate")
	if got != want {
		t.Fatalf("语料目录 = %q, want %q", got, want)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("语料目录应已创建: info=%v err=%v", info, err)
	}
}

func TestEnsureSimulationSourceDirRejectsEmptyBookPath(t *testing.T) {
	if _, err := ensureSimulationSourceDir("  "); err == nil {
		t.Fatal("空书目录不能回退到进程工作目录")
	}
}

func TestImportSimulationSourcesCopiesAndUpdates(t *testing.T) {
	sourceRoot := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), "simulate")
	source := filepath.Join(sourceRoot, "参考小说.txt")
	if err := os.WriteFile(source, []byte("第一版"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := importSimulationSources(targetDir, []string{source})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "参考小说.txt" {
		t.Fatalf("导入结果不正确: %v", files)
	}
	target := filepath.Join(targetDir, "参考小说.txt")
	if data, err := os.ReadFile(target); err != nil || string(data) != "第一版" {
		t.Fatalf("首次导入内容不正确: data=%q err=%v", data, err)
	}

	if err := os.WriteFile(source, []byte("第二版"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importSimulationSources(targetDir, []string{source}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "第二版" {
		t.Fatalf("更新导入内容不正确: data=%q err=%v", data, err)
	}
}

func TestImportSimulationSourcesRejectsUnsupportedBatchBeforeCopy(t *testing.T) {
	sourceRoot := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), "simulate")
	valid := filepath.Join(sourceRoot, "参考.txt")
	invalid := filepath.Join(sourceRoot, "图片.png")
	if err := os.WriteFile(valid, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importSimulationSources(targetDir, []string{valid, invalid}); err == nil {
		t.Fatal("混入不支持格式时应拒绝整批导入")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "参考.txt")); !os.IsNotExist(err) {
		t.Fatalf("校验失败前不应复制任何文件: %v", err)
	}
}

func TestCoCreateOldCompletionDoesNotForgetReplacement(t *testing.T) {
	var jobs coCreateJobs
	first, firstID := jobs.begin()
	second, _ := jobs.begin()
	if first.Err() == nil {
		t.Fatal("新一轮共创应取消旧一轮")
	}
	jobs.end(firstID)
	if !jobs.active() {
		t.Fatal("旧共创返回不应清掉新一轮的取消句柄")
	}
	jobs.abort()
	if second.Err() == nil {
		t.Fatal("新一轮共创仍应能被取消")
	}
}

func TestAskBridgePendingCanBeRecovered(t *testing.T) {
	emitted := make(chan struct{}, 1)
	b := newAskBridge(func(string, []askQuestion) { emitted <- struct{}{} })
	questions := []tools.Question{{
		Question: "继续吗？", Header: "方向",
		Options: []tools.Option{{Label: "继续", Description: "继续写"}, {Label: "暂停", Description: "暂不写"}},
	}}
	result := make(chan *tools.AskUserResponse, 1)
	go func() {
		resp, _ := b.handler(context.Background(), questions)
		result <- resp
	}()
	<-emitted

	pending := b.pendingRequest()
	if pending == nil || pending.ID == "" || len(pending.Questions) != 1 {
		t.Fatalf("应能补取未回答问题，实际 %+v", pending)
	}
	if err := b.answer(pending.ID, map[string]string{"继续吗？": "继续"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got == nil || got.Answers["继续吗？"] != "继续" {
		t.Fatalf("补取后回答未送回引擎: %+v", got)
	}
	if err := b.answer(pending.ID, map[string]string{"继续吗？": "暂停"}, nil); err == nil {
		t.Fatal("同一提问的重复回答应被拒绝")
	}
}

func TestPrepareProviderConfigRepairsMissingDefault(t *testing.T) {
	draft := host.ModelConfigurationDraft{
		Provider:     "custom-proxy",
		Type:         "openai",
		BaseURL:      "https://example.invalid/v1",
		Models:       []bootstrap.ModelConfig{{Name: "gpt-test"}},
		APIKeyAction: host.APIKeyReplace,
		APIKey:       "test-key",
	}
	candidate, _, fullSave, err := prepareProviderConfig(bootstrap.Config{}, draft)
	if err != nil {
		t.Fatalf("空/损坏配置应能通过新增 provider 修复: %v", err)
	}
	if candidate.Provider != draft.Provider || candidate.ModelName != "gpt-test" {
		t.Fatalf("默认选择未修复: provider=%q model=%q", candidate.Provider, candidate.ModelName)
	}
	if !fullSave {
		t.Fatal("修复顶层默认选择必须整份写回，不能只保存 providers 补丁")
	}
}

// 技能中心的绑定在未打开书时必须给出可读错误，而不是 panic——
// 前端的技能面板可以在书库页被误点开。
func TestSkillBindingsWithoutOpenBook(t *testing.T) {
	a := NewApp(versionForTest())

	if _, err := a.ListSkills(); err == nil {
		t.Fatal("未打开书时 ListSkills 应报错")
	}
	if _, err := a.ReloadSkills(); err == nil {
		t.Fatal("未打开书时 ReloadSkills 应报错")
	}
	if _, err := a.OpenSkillsDir(); err == nil {
		t.Fatal("未打开书时 OpenSkillsDir 应报错")
	}
	if err := a.RunSkill("anti-ai-tone", []int{1}); err == nil {
		t.Fatal("未打开书时 RunSkill 应报错")
	}
}

// SkillCatalog 的 problems / skills 必须序列化成数组而非 null：
// 前端用 (x ?? []) 兜底，但 problems 为 null 时 length 判断会静默跳过告警。
func TestSkillCatalogJSONContract(t *testing.T) {
	data, err := json.Marshal(SkillCatalog{
		Skills:   []SkillItem{},
		Dir:      "/home/u/.ainovel/skills",
		Problems: []SkillProblem{{Source: "global:bad.md", Err: "scope 非法"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"skills", "dir", "problems"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("SkillCatalog 缺少前端依赖的字段 %q: %s", key, data)
		}
	}
	probs, ok := got["problems"].([]any)
	if !ok || len(probs) != 1 {
		t.Fatalf("problems 应是数组: %s", data)
	}
	first := probs[0].(map[string]any)
	if first["source"] != "global:bad.md" || first["err"] != "scope 非法" {
		t.Fatalf("SkillProblem 字段名与前端不符: %s", data)
	}
}

// SkillItem 的 JSON 字段名是前端手写门面（bindings/wails.ts）依赖的契约，
// 改名会让面板静默显示空白，因此在这里钉住。
func TestSkillItemJSONContract(t *testing.T) {
	data, err := json.Marshal(SkillItem{
		Name: "anti-ai-tone", Description: "清 AI 味", Agent: "editor",
		Scope: "chapters", Source: "builtin:anti-ai-tone.md", Body: "判据",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "description", "agent", "scope", "source", "body"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("SkillItem 缺少前端依赖的字段 %q: %s", key, data)
		}
	}
}

func TestWriteCoverFailurePreservesOldFormat(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(old, []byte("old-cover"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 用同名目录占住新格式目标，使最终 Rename 稳定失败；临时文件写入仍能成功。
	if err := os.Mkdir(filepath.Join(dir, "cover.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := coverSpec{Prompt: "prompt", Model: "model", Platform: "tomato"}
	if err := writeCoverComposite(dir, []byte("new-cover"), "image/png", spec, nil); err == nil {
		t.Fatal("新封面目标不可替换时应返回错误")
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("新封面失败后旧封面必须保留: %v", err)
	}
	if string(data) != "old-cover" {
		t.Fatalf("旧封面内容被破坏: %q", data)
	}
}
