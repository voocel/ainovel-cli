package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// App is a composition root. Attaching use cases to it would recreate Service
// even if the implementation were spread across files.
func TestBootstrapRemainsCompositionOnly(t *testing.T) {
	dir := filepath.Join("..", "bootstrap")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil {
				t.Errorf("bootstrap/%s: method %s belongs to a use-case component, not the composition root", entry.Name(), fn.Name)
			}
		}
	}
}
