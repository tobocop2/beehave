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
	methods, err := scanFiles(dir, false, func(_ *token.FileSet, file *ast.File) []string { return methodsOf(file, typeName) })
	if err != nil {
		return nil, err
	}

	slices.Sort(methods)

	return methods, nil
}

// scanFiles parses the production files of a folder, or its test files when tests is true, and joins what read returns for each file.
func scanFiles(dir string, tests bool, read func(*token.FileSet, *ast.File) []string) ([]string, error) {
	paths, err := goFiles(dir, tests)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()

	var found []string

	for _, path := range paths {
		file, parseErr := parseSource(fset, path)
		if parseErr != nil {
			return nil, parseErr
		}

		found = append(found, read(fset, file)...)
	}

	return found, nil
}

// UnwrappedCalls lists the calls pkg.ctor(...) in the test files of a folder whose second argument is not a call of the function wrapper, as file:line:function. A test uses it to require
// that every adapter of a test is built over a runner that the wrapper makes strict, so no test can reach gh through a fake that skips the table.
func UnwrappedCalls(dir, pkg, ctor, wrapper string) ([]string, error) {
	return scanFiles(dir, true, func(fset *token.FileSet, file *ast.File) []string { return unwrappedIn(fset, file, pkg, ctor, wrapper) })
}

// unwrappedIn lists the calls pkg.ctor(...) of one file whose second argument is not a call of the wrapper.
func unwrappedIn(fset *token.FileSet, file *ast.File, pkg, ctor, wrapper string) []string {
	var found []string

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, isCall := n.(*ast.CallExpr); isCall && isCallOf(call, pkg, ctor) && !isCallOf(argCall(call.Args[1]), "", wrapper) {
				position := fset.Position(call.Pos())
				found = append(found, fmt.Sprintf("%s:%d:%s", filepath.Base(position.Filename), position.Line, fn.Name.Name))
			}

			return true
		})
	}

	return found
}

// argCall returns the argument when it is a call, and nil otherwise.
func argCall(arg ast.Expr) *ast.CallExpr {
	if call, ok := arg.(*ast.CallExpr); ok {
		return call
	}

	return nil
}

// isCallOf says whether the call is pkg.name(...) with two arguments at least, or name(...) with one argument at least when pkg is empty. A nil call is no call.
func isCallOf(call *ast.CallExpr, pkg, name string) bool {
	if call == nil || len(call.Args) < 1 {
		return false
	}

	if pkg == "" {
		id, ok := call.Fun.(*ast.Ident)

		return ok && id.Name == name
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	id, named := sel.X.(*ast.Ident)

	return named && id.Name == pkg && sel.Sel.Name == name && len(call.Args) >= 2
}

// goFiles lists the paths of the Go files of a folder: the test files when tests is true, and the others when it is false.
func goFiles(dir string, tests bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var paths []string

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && strings.HasSuffix(name, "_test.go") == tests {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	return paths, nil
}

// parseSource parses one Go file without resolving objects.
func parseSource(fset *token.FileSet, path string) (*ast.File, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
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
