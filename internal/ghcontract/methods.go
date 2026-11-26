package ghcontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PublicMethods lists the exported methods of the type in the production files of a folder, sorted. A test uses it to require a driver for each method of an adapter, so a new method cannot skip the contract.
func PublicMethods(dir, typeName string) ([]string, error) {
	paths, err := sourceFiles(dir)
	if err != nil {
		return nil, err
	}

	var methods []string

	for _, path := range paths {
		file, parseErr := parseSource(path)
		if parseErr != nil {
			return nil, parseErr
		}

		methods = append(methods, methodsOf(file, typeName)...)
	}

	slices.Sort(methods)

	return methods, nil
}

// sourceFiles lists the paths of the Go files of a folder that are not test files.
func sourceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var paths []string

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	return paths, nil
}

// parseSource parses one Go file without resolving objects.
func parseSource(path string) (*ast.File, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	return file, nil
}

// methodsOf lists the exported methods of the type that one file declares.
func methodsOf(file *ast.File, typeName string) []string {
	var methods []string

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.IsExported() && receiverName(fn.Recv.List[0].Type) == typeName {
			methods = append(methods, fn.Name.Name)
		}
	}

	return methods
}

// receiverName is the name of a receiver type, with a pointer mark removed.
func receiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}

	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}

	return ""
}
