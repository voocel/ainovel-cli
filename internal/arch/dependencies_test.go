// Package arch 只承载架构纪律的自动化断言，不含生产代码。
package arch

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
	"arch":   {},
	"domain": {},
	// activity 是运行时平面的实时活动通道（页面设计 §4）：叶子包，capability 发布、
	// service 订阅、entry 消费快照类型；权威语义与持久化不经过它。
	"activity":          {},
	"store":             {"domain"},
	"change":            {"domain", "store"},
	"derive":            {"domain"},
	"workspace":         {"domain", "store"},
	"llm/models":        {"domain"},
	"capability/pack":   {"domain"},
	"capability/prompt": {"domain", "store"},
	"capability":        {"activity", "domain", "store", "change", "capability/prompt", "workspace"},
	"operation":         {"domain", "store", "change", "capability/prompt"},
	"service":           {"activity", "domain", "store", "change", "derive", "operation", "capability/pack", "capability/prompt"},
	"entry/app":         {},
	"entry/headless":    {"domain", "service"},
	"entry/tui":         {"activity", "domain", "service", "entry/app", "entry/headless"},
}

// allowedTestImports 是测试文件在生产白名单之外额外允许的依赖，用于搭建测试环境
// （例如打开一个真实 SQLite）。它与生产白名单严格分开：测试可以用 store 搭台子，
// 与"UI 层可以直连 store"是两件事，混为一谈会让规则失去意义。
var allowedTestImports = map[string][]string{
	// headless/tui 的用例需要真实存储装配 Service；生产代码仍只依赖 service。
	"entry/headless": {"store"},
	"entry/tui":      {"store"},
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

// TestDomainHasNoInternalDependencies 单独钉住地基：domain 是所有层的共同依赖，
// 一旦它反向依赖任何包，整张依赖图立刻失去方向。
func TestDomainHasNoInternalDependencies(t *testing.T) {
	pkg, err := build.ImportDir(filepath.Join("..", "domain"), 0)
	if err != nil {
		t.Fatalf("import domain: %v", err)
	}
	for _, imported := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
		if strings.HasPrefix(imported, modulePath+"/") {
			t.Errorf("domain 必须零内部依赖，实际依赖 %s", imported)
		}
	}
}
