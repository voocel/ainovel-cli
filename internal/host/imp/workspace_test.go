package imp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestStableAndDistinct(t *testing.T) {
	a := Digest([]byte("第一章"))
	if a != Digest([]byte("第一章")) {
		t.Fatal("同输入 digest 不稳定")
	}
	if a == Digest([]byte("第二章")) {
		t.Fatal("不同输入 digest 相同")
	}
	if len(a) < 8 || a[:7] != "sha256:" {
		t.Fatalf("digest 前缀不符：%s", a)
	}
}

func TestWorkspaceAtomicRoundtrip(t *testing.T) {
	w := &Workspace{dir: t.TempDir()}
	if err := w.writeAtomic("nested/x.txt", []byte("hello")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	got, err := os.ReadFile(w.path("nested/x.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("读回不符：%q %v", got, err)
	}
}

func TestArtifactRoundtripPreservesIdentity(t *testing.T) {
	w := &Workspace{dir: t.TempDir()}
	type payload struct {
		N int `json:"n"`
	}
	if err := writeArtifact(w, "seg.json", "sha256:abc", payload{N: 7}); err != nil {
		t.Fatalf("writeArtifact: %v", err)
	}
	a, err := readArtifact[payload](w, "seg.json")
	if err != nil {
		t.Fatalf("readArtifact: %v", err)
	}
	if a.InputDigest != "sha256:abc" || a.Payload.N != 7 || a.SchemaVersion != workspaceSchemaVersion {
		t.Fatalf("身份未保留：%+v", a)
	}
}

func TestReadArtifactRejectsSchemaMismatch(t *testing.T) {
	w := &Workspace{dir: t.TempDir()}
	// 直接写一个 schema 版本不匹配的工件。
	raw := Artifact[string]{SchemaVersion: 999, InputDigest: "sha256:x", Payload: "y"}
	if err := w.writeJSON("seg.json", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact[string](w, "seg.json"); err == nil {
		t.Fatal("schema 版本不匹配应被拒绝")
	}
}

func TestCreateWorkspacePublishesAtomically(t *testing.T) {
	book := t.TempDir()
	norm := []byte("第一章\n正文\n")
	m := Manifest{
		Version:          workspaceSchemaVersion,
		SourceName:       "book.txt",
		NormalizedSHA256: Digest(norm),
		Encoding:         encodingUTF8,
	}
	ws, err := createWorkspace(book, m, Intent{Version: workspaceSchemaVersion}, norm)
	if err != nil {
		t.Fatalf("createWorkspace: %v", err)
	}
	if !ws.Active() {
		t.Fatal("发布后工作区应为活动")
	}
	for _, f := range []string{fileManifest, fileIntent, fileSource} {
		if !ws.has(f) {
			t.Fatalf("缺工件 %s", f)
		}
	}
	// createWorkspace 成功后不应泄漏半初始化临时目录（meta/import.init-*）。
	if dirs, _ := filepath.Glob(filepath.Join(book, "meta", "import.init-*")); len(dirs) != 0 {
		t.Fatalf("发布成功后不应残留 init 目录：%v", dirs)
	}
	// 重复创建应因已存在而失败。
	if _, err := createWorkspace(book, m, Intent{}, norm); err == nil {
		t.Fatal("已存在活动工作区时重复创建应失败")
	}
}

func TestCreateWorkspaceRejectsInconsistentSnapshot(t *testing.T) {
	book := t.TempDir()
	m := Manifest{Version: workspaceSchemaVersion, NormalizedSHA256: Digest([]byte("A"))}
	// manifest 声明的摘要与实际写入的 normalized 不一致 → 发布前校验应拦截。
	if _, err := createWorkspace(book, m, Intent{}, []byte("B")); err == nil {
		t.Fatal("源快照与 manifest 摘要不一致时应拒绝发布")
	}
	if _, err := os.Stat(filepath.Join(book, "meta", "import")); !os.IsNotExist(err) {
		t.Fatal("发布失败后不应留下活动工作区")
	}
}
