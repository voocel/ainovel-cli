package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// app 是编排层，只能经 change 引擎写权威数据（§11 模块纪律第 3 条）。它直接
// 持有具体 store，白名单挡不住绕过引擎的提案写入，因此按方法名钉住：
// change.Store 契约（domain/change/ports.go）里的写方法不得出现在 app 生产代码。
var authorityWriteMethods = []string{
	"SaveProposal", "SaveExecutionProposal", "CommitProposal", "CommitExecutionProposal", "RejectProposal",
}

func TestAppPackagesWriteAuthorityOnlyThroughChangeEngine(t *testing.T) {
	root := filepath.Join("..", "app")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains(authorityWriteMethods, selector.Sel.Name) {
				t.Errorf("%s 调用了 %s：app 只能经 change 引擎写权威数据", path, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
