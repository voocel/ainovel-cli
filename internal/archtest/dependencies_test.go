// Package archtest 只承载架构纪律的自动化断言，不含生产代码。
package archtest

import (
	"errors"
	"fmt"
	"go/build"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/voocel/ainovel-cli"

// allowedImports 是每个内部包的生产代码允许直接依赖的其它内部包白名单。
//
// v1-architecture-plan §12.1：依赖只向下、无环，任何反向引用都是设计错误。仅靠文档
// 守不住——capability → operation 曾经破例并存活至今无人发现，因此把纪律落成测试。
//
// 新增包必须在此显式登记，让"它可以依赖谁"成为一次明确的 review 决定，而不是随手
// import 的既成事实。
var allowedImports = map[string][]string{
	"archtest":                {},
	"domain/model":            {},
	"infra/activity":          {},
	"infra/store":             {"domain/model"},
	"domain/change":           {"domain/model"},
	"domain/derive":           {"domain/model"},
	"infra/workspace":         {"domain/model", "infra/store"},
	"infra/llm":               {"domain/model", "infra/llm/models"},
	"infra/llm/models":        {"domain/model"},
	"infra/jsonc":             {"domain/model"},
	"infra/capability/pack":   {"domain/model", "infra/jsonc"},
	"infra/capability/prompt": {"domain/model", "infra/store"},
	"infra/capability":        {"infra/activity", "domain/model", "infra/store", "domain/change", "infra/capability/prompt", "infra/workspace", "infra/llm", "infra/llm/models"},
	"domain/operation":        {"domain/model", "domain/change"},
	"app/project":             {"domain/model", "infra/store", "domain/change"},
	"app/diag":                {"domain/model", "infra/store"},
	"app/resource":            {"domain/model", "infra/store", "domain/change", "app/project", "infra/capability/pack", "infra/capability/prompt"},
	"app/profile":             {"domain/model", "infra/store", "app/project", "app/resource", "domain/derive", "infra/capability/prompt"},
	"app/task":                {"domain/creation", "domain/model", "infra/store", "app/project", "app/resource", "app/profile", "domain/operation", "infra/capability/prompt"},
	// The driver cannot depend on application snapshots, evidence interpretation,
	// novel policies, presentation queries, or application assembly.
	"domain/creation": {"domain/model"},
	"app/evidence":    {"domain/model", "infra/store", "domain/change", "domain/operation"},
	"app/novel":       {"domain/model", "infra/store", "domain/change", "domain/creation", "app/project", "app/resource", "app/task"},
	"app/decision":    {"domain/model", "infra/store", "domain/change", "app/project", "app/resource", "app/task"},
	"app/workbench":   {"infra/activity", "domain/model", "infra/store", "domain/creation", "app/decision", "app/novel", "app/project"},
	// 模型绑定用例只认配置与模型适配器，不认任务与作品。
	"app/binding":    {"infra/config", "infra/llm", "infra/llm/models", "infra/capability/prompt"},
	"bootstrap":      {"app/binding", "app/diag", "domain/model", "infra/store", "domain/change", "domain/creation", "app/decision", "app/evidence", "app/novel", "domain/operation", "app/profile", "app/project", "app/resource", "app/task", "app/workbench", "infra/activity", "infra/capability"},
	"infra/config":   {},
	"entry/headless": {"app/binding", "app/diag", "domain/model", "bootstrap", "domain/creation", "app/decision", "app/novel", "app/profile", "app/project", "app/resource", "app/task", "infra/jsonc"},
	"entry/tui":      {"app/binding", "app/diag", "infra/activity", "domain/model", "bootstrap", "app/decision", "app/novel", "app/project", "app/workbench", "infra/config", "infra/jsonc"},
}

// Tests may open a real store for assembly; entry production code may not.
var allowedTestImports = map[string][]string{
	"app/task":         {"domain/change"},
	"domain/change":    {"infra/store"},
	"domain/operation": {"infra/store"},
	"domain/creation":  {"infra/store"},
	"bootstrap":        {"infra/capability/prompt", "domain/derive", "infra/config", "infra/llm/models"},
	"entry/headless":   {"infra/store", "infra/config", "infra/llm/models"},
	"entry/tui":        {"infra/store", "infra/llm/models"},
}

func TestInternalPackagesRespectDependencyDirection(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve internal root: %v", err)
	}
	visited := make(map[string]bool, len(allowedImports))
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() {
			return walkErr
		}
		pkg, importErr := build.ImportDir(path, 0)
		if importErr != nil {
			var noGo *build.NoGoError
			if errors.As(importErr, &noGo) {
				return nil // 纯中间目录，无 Go 文件
			}
			return fmt.Errorf("%s: %w", path, importErr)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		allowed, registered := allowedImports[name]
		if !registered {
			t.Errorf("包 %s 未在 allowedImports 登记：新增包必须显式声明它允许依赖哪些内部包", name)
			return nil
		}
		visited[name] = true

		checkImports(t, name, pkg.Imports, allowed, "生产代码")
		checkLayerDirection(t, name, pkg.Imports)
		testAllowed := slices.Concat(allowed, allowedTestImports[name])
		checkImports(t, name, slices.Concat(pkg.TestImports, pkg.XTestImports), testAllowed, "测试代码")
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal packages: %v", walkErr)
	}
	for name := range allowedImports {
		if !visited[name] {
			t.Errorf("allowedImports 登记的包 %s 已不存在，请清理白名单", name)
		}
	}
}

func checkImports(t *testing.T, name string, imports, allowed []string, kind string) {
	t.Helper()
	slices.Sort(imports)
	for _, imported := range slices.Compact(imports) {
		dependency, internal := strings.CutPrefix(imported, modulePath+"/internal/")
		if !internal || dependency == name {
			continue
		}
		if !slices.Contains(allowed, dependency) {
			t.Errorf("包 %s 的%s不得依赖 %s；允许的依赖为 %v", name, kind, dependency, allowed)
		}
	}
}

// TestModelHasNoInternalDependencies 单独钉住地基：model 是所有层的共同依赖，
// 一旦它反向依赖任何包，整张依赖图立刻失去方向。
func TestModelHasNoInternalDependencies(t *testing.T) {
	pkg, err := build.ImportDir(filepath.Join("..", "domain", "model"), 0)
	if err != nil {
		t.Fatalf("import domain/model: %v", err)
	}
	for _, imported := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
		if strings.HasPrefix(imported, modulePath+"/") {
			t.Errorf("domain/model 必须零内部依赖，实际依赖 %s", imported)
		}
	}
}

// Layer restrictions remain independent of package allowlists so a new entry
// cannot silently reverse the architecture's direction.
func checkLayerDirection(t *testing.T, name string, imports []string) {
	t.Helper()
	layer, _, _ := strings.Cut(name, "/")
	for _, imported := range imports {
		dependency, internal := strings.CutPrefix(imported, modulePath+"/internal/")
		if !internal {
			continue
		}
		target, _, _ := strings.Cut(dependency, "/")
		forbidden := false
		switch layer {
		case "domain":
			forbidden = target != "domain"
		case "infra":
			forbidden = target != "domain" && target != "infra"
		case "app":
			forbidden = target != "domain" && target != "infra" && target != "app"
		}
		if forbidden {
			t.Errorf("包 %s 不得依赖 %s：违反 %s 层的生产依赖方向", name, dependency, layer)
		}
	}
}
