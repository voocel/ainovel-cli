package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestCoordinatorDoesNotReferenceControlDocumentKinds 守护 D33 纪律：协调器只
// 推导下一步，控制类文档（Ownership/Approval/Overlay/Assets/Directive）由
// Change Engine 消化、由装配层带进任务，service/run.go 不得按它们分支。
func TestCoordinatorDoesNotReferenceControlDocumentKinds(t *testing.T) {
	path := filepath.Join("..", "service", "run.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	forbidden := map[string]bool{
		"DocumentOwnership": true, "DocumentApproval": true, "DocumentOverlay": true,
		"DocumentAssets": true, "DocumentDirective": true,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "domain" && forbidden[selector.Sel.Name] {
			t.Errorf("service/run.go 引用了控制类文档常量 domain.%s：协调器不得按控制文档分支（D33）", selector.Sel.Name)
		}
		return true
	})
}
