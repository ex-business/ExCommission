package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDomainBoundaries(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		if strings.Contains(filepath.ToSlash(path), "/domain/") {
			for _, imp := range file.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				if name == "net/http" || name == "database/sql" || strings.Contains(name, "/adapters") || strings.Contains(name, "/application") {
					t.Errorf("domain boundary: %s imports %s", path, name)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && (ident.Name == "float32" || ident.Name == "float64") {
				t.Errorf("float forbidden: %s", path)
			}
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.FLOAT {
				t.Errorf("float literal forbidden: %s", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
