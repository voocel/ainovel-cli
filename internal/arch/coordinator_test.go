package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoordinatorDoesNotReferenceControlDocumentKinds 守护 D33/D49 纪律：协调器只做
// 机制。控制类文档（Ownership/Approval/Overlay/Assets/Directive）由 Change Engine 消化、
// 由装配层带进任务；小说规则（目标载荷、章节计划、写作类 Operation）只住在推导器里，
// service/run.go 不得按它们分支。
func TestCoordinatorDoesNotReferenceControlDocumentKinds(t *testing.T) {
	path := filepath.Join("..", "service", "run.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	forbidden := map[string]string{
		"DocumentOwnership": "D33", "DocumentApproval": "D33", "DocumentOverlay": "D33",
		"DocumentAssets": "D33", "DocumentDirective": "D33", "DocumentAdjudication": "D43",
		"NovelGoal": "D49", "DecodeNovelGoal": "D49", "GoalNovel": "D49", "PlanChapter": "D49",
		"OperationDevelopPlan": "D49", "OperationRevisePlan": "D49", "OperationWriteChapter": "D49",
		"OperationRewriteChapter": "D49", "OperationReviewRange": "D49",
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "domain" {
			if decision, banned := forbidden[selector.Sel.Name]; banned {
				t.Errorf("service/run.go 引用了 domain.%s：协调器只做机制，不得按它分支（%s）", selector.Sel.Name, decision)
			}
		}
		return true
	})
}

// TestNovelDeriverStaysPure 守护 D49：小说推导器是纯函数，不触存储也不触执行引擎。
func TestNovelDeriverStaysPure(t *testing.T) {
	path := filepath.Join("..", "service", "novel_goal.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, spec := range file.Imports {
		switch strings.Trim(spec.Path.Value, `"`) {
		case "github.com/voocel/ainovel-cli/internal/store", "github.com/voocel/ainovel-cli/internal/operation":
			t.Errorf("service/novel_goal.go 依赖了 %s：推导器只读快照与证据，不得触存储或执行引擎（D49）", spec.Path.Value)
		}
	}
}
