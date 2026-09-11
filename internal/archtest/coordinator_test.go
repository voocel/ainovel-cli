package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D33/D49 applies to the whole driver package, so moving a business branch into
// another file cannot circumvent the boundary. State-loading goal adapters and
// pure application policies live outside creation.
func TestCoordinatorDoesNotReferenceControlDocumentKinds(t *testing.T) {
	root := filepath.Join("..", "domain", "creation")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]string{
		"DocumentOwnership": "D33", "DocumentApproval": "D33", "DocumentOverlay": "D33",
		"DocumentAssets": "D33", "DocumentDirective": "D33", "DocumentAdjudication": "D43",
		"DocumentIntent": "D49", "DocumentPlan": "D49", "DocumentEntity": "D49",
		"DocumentCanon": "D49", "DocumentManuscript": "D49", "DocumentAttachment": "D49",
		"NovelGoal": "D49", "DecodeNovelGoal": "D49", "GoalNovel": "D49",
		"Intent": "D49", "PlanNode": "D49", "CanonFact": "D49", "ManuscriptChapter": "D49",
		"ReviewVerdict": "D49", "ReviewFinding": "D49", "PlanChapter": "D49",
		"OperationInitializeProject": "D49", "OperationDevelopPlan": "D49", "OperationRevisePlan": "D49",
		"OperationWriteChapter": "D49", "OperationRewriteChapter": "D49", "OperationRewriteAffected": "D49",
		"OperationReviewRange": "D49", "OperationReviseCanon": "D49",
		"OperationGenerateAsset": "D49", "OperationInspectAsset": "D49",
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		domainAlias := "model"
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, `"`) == modulePath+"/internal/domain/model" && spec.Name != nil {
				domainAlias = spec.Name.Name
				if domainAlias == "." {
					t.Errorf("%s 不得点导入 domain，业务符号必须可审查", path)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == domainAlias {
				if decision, banned := forbidden[selector.Sel.Name]; banned {
					t.Errorf("%s 引用了 model.%s：协调器只做机制，不得按业务类型分支（%s）", path, selector.Sel.Name, decision)
				}
			}
			return true
		})
	}
}

// The novel policy can inspect snapshots and return a step; I/O belongs to Goal.
func TestNovelDeriverStaysPure(t *testing.T) {
	path := filepath.Join("..", "app", "novel", "novel_goal.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, spec := range file.Imports {
		imported := strings.Trim(spec.Path.Value, `"`)
		dependency, internal := strings.CutPrefix(imported, modulePath+"/internal/")
		if !internal {
			continue
		}
		switch dependency {
		case "domain/model", "app/project", "domain/creation":
		default:
			t.Errorf("%s 依赖了 %s：纯规则只读快照并返回步骤，不能加载状态或执行任务（D49）", path, imported)
		}
	}
}
