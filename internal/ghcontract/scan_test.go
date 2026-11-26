package ghcontract_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
)

// The scan reads the field lists that production code gives to PRView and PRList. About thirty fakes of the forge accept any field, so the check lives where the
// list is written. The scan resolves a string literal, a constant, a local variable, a slice literal, append, strings.Split, a function that returns a slice, and a
// parameter through the calls of its function. Anything else is unresolved, and an unresolved field list fails the test at its call site: write it as a literal.

// unresolved stands for an element that the scan cannot read from the source.
const unresolved = "\x00unresolved"

const maxDepth = 12

// alternative separates the readings of a value that has more than one.
const alternative = "\x01"

// site is a call with the function that holds it.
type site struct {
	call *ast.CallExpr
	fn   *ast.FuncDecl
}

// pkg is the production code of one folder.
type pkg struct {
	fset   *token.FileSet
	values map[string]ast.Expr
	funcs  map[string][]*ast.FuncDecl
	calls  map[string][]site
	sites  []site
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}

	return ""
}

func loadPkg(fset *token.FileSet, files []*ast.File) *pkg {
	p := &pkg{fset: fset, values: map[string]ast.Expr{}, funcs: map[string][]*ast.FuncDecl{}, calls: map[string][]site{}}

	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				p.funcs[d.Name.Name] = append(p.funcs[d.Name.Name], d)
				p.addCalls(d)
			case *ast.GenDecl:
				p.addValues(d)
			}
		}
	}

	return p
}

func (p *pkg) addValues(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) == len(v.Values) {
			for i, name := range v.Names {
				p.values[name.Name] = v.Values[i]
			}
		}
	}
}

func (p *pkg) addCalls(fn *ast.FuncDecl) {
	if fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			s := site{call: call, fn: fn}
			p.calls[callName(call.Fun)] = append(p.calls[callName(call.Fun)], s)
			p.sites = append(p.sites, s)
		}

		return true
	})
}

// resolver reads the elements that an expression holds, inside one function.
type resolver struct {
	p     *pkg
	fn    *ast.FuncDecl
	seen  map[string]bool
	depth int
}

func (p *pkg) resolver(fn *ast.FuncDecl, depth int) *resolver {
	return &resolver{p: p, fn: fn, seen: map[string]bool{}, depth: depth}
}

func (r *resolver) exprs(list []ast.Expr) []string {
	var out []string
	for _, e := range list {
		out = append(out, r.strs(e)...)
	}

	return out
}

func (r *resolver) strs(e ast.Expr) []string {
	if r.depth > maxDepth {
		return []string{unresolved}
	}

	switch e := e.(type) {
	case *ast.BasicLit:
		if text, err := strconv.Unquote(e.Value); err == nil && e.Kind == token.STRING {
			return []string{text}
		}
	case *ast.ParenExpr:
		return r.strs(e.X)
	case *ast.BinaryExpr:
		left, right := r.strs(e.X), r.strs(e.Y)
		if e.Op == token.ADD && len(left) == 1 && len(right) == 1 && left[0] != unresolved && right[0] != unresolved {
			return []string{left[0] + right[0]}
		}
	case *ast.CompositeLit:
		return r.exprs(e.Elts)
	case *ast.Ident:
		return r.ident(e.Name)
	case *ast.CallExpr:
		return r.call(e)
	}

	return []string{unresolved}
}

// ident reads a name: a local assignment first, then a parameter, then a package value.
func (r *resolver) ident(name string) []string {
	if r.seen[name] {
		return nil
	}

	r.seen[name] = true
	defer delete(r.seen, name)

	if rhs := r.assigned(name); len(rhs) > 0 {
		return r.exprs(rhs)
	}

	if index, variadic := r.param(name); index >= 0 {
		return r.callers(index, variadic)
	}

	if v, ok := r.p.values[name]; ok {
		return r.strs(v)
	}

	return []string{unresolved}
}

// assigned lists every expression that the function assigns to the local name.
func (r *resolver) assigned(name string) []ast.Expr {
	var rhs []ast.Expr

	if r.fn == nil || r.fn.Body == nil {
		return nil
	}

	ast.Inspect(r.fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, l := range s.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name && len(s.Lhs) == len(s.Rhs) {
					rhs = append(rhs, s.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, id := range s.Names {
				if id.Name == name && len(s.Names) == len(s.Values) {
					rhs = append(rhs, s.Values[i])
				}
			}
		}

		return true
	})

	return rhs
}

// param returns the position of the name among the parameters of the function, or -1.
func (r *resolver) param(name string) (int, bool) {
	if r.fn == nil {
		return -1, false
	}

	index := 0

	for _, field := range r.fn.Type.Params.List {
		_, variadic := field.Type.(*ast.Ellipsis)

		for _, id := range field.Names {
			if id.Name == name {
				return index, variadic
			}

			index++
		}
	}

	return -1, false
}

// callers reads the argument at the position in every call of the function in the folder.
func (r *resolver) callers(index int, variadic bool) []string {
	sites := r.p.calls[r.fn.Name.Name]
	if variadic || len(sites) == 0 {
		return []string{unresolved}
	}

	var alts [][]string

	for _, s := range sites {
		if len(s.call.Args) <= index {
			return []string{unresolved}
		}

		alts = append(alts, r.p.resolver(s.fn, r.depth+1).strs(s.call.Args[index]))
	}

	return union(alts)
}

// union joins alternative readings into one element, so a value that holds one of several lists is still one element. A single reading stays as it is.
func union(alts [][]string) []string {
	switch len(alts) {
	case 0:
		return []string{unresolved}
	case 1:
		return alts[0]
	}

	var all []string
	for _, alt := range alts {
		all = append(all, alt...)
	}

	return []string{strings.Join(all, alternative)}
}

func (r *resolver) call(e *ast.CallExpr) []string {
	switch name := callName(e.Fun); {
	case name == "append":
		return r.exprs(e.Args)
	case name == "Split" && len(e.Args) == 2:
		text, sep := r.strs(e.Args[0]), r.strs(e.Args[1])
		if len(text) == 1 && len(sep) == 1 && text[0] != unresolved && sep[0] != unresolved {
			return strings.Split(text[0], sep[0])
		}
	default:
		return r.returned(name)
	}

	return []string{unresolved}
}

// returned reads the values that the functions of that name in the folder return.
func (r *resolver) returned(name string) []string {
	decls := r.p.funcs[name]
	if len(decls) == 0 {
		return []string{unresolved}
	}

	var alts [][]string

	for _, decl := range decls {
		sub := r.p.resolver(decl, r.depth+1)

		if decl.Body == nil {
			continue
		}

		ast.Inspect(decl.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok {
				alts = append(alts, sub.exprs(ret.Results))
			}

			return true
		})
	}

	return union(alts)
}

// scan reads every production file under root. It returns the findings and the number of PRView and PRList calls it read.
func scan(t *testing.T, root string) ([]string, int) {
	t.Helper()

	folders := map[string][]*ast.File{}
	fset := token.NewFileSet()

	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := entry.Name()
		if entry.IsDir() && path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}

		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}

		folders[filepath.Dir(path)] = append(folders[filepath.Dir(path)], file)

		return nil
	}))

	var (
		findings []string
		calls    int
	)

	for _, files := range folders {
		p := loadPkg(fset, files)

		for _, s := range p.sites {
			if name := callName(s.call.Fun); (name == "PRView" || name == "PRList") && len(s.call.Args) >= 2 {
				calls++

				findings = append(findings, p.judge(root, name, s)...)
			}
		}
	}

	slices.Sort(findings)

	return findings, calls
}

// judge checks the fields of one call against the shape of gh that the method runs.
func (p *pkg) judge(root, method string, s site) []string {
	shape := "pr view"
	if method == "PRList" {
		shape = "pr list"
	}

	var allowed []string

	for _, known := range ghcontract.Shapes() {
		if strings.Join(known.Words, " ") == shape {
			allowed = known.Fields
		}
	}

	position := p.fset.Position(s.call.Pos())
	where := fmt.Sprintf("%s:%d", relative(root, position.Filename), position.Line)
	elems := p.resolver(s.fn, 0).exprs(s.call.Args[2:])

	if method == "PRView" {
		return fieldFindings(where, shape, allowed, elems)
	}

	var out []string

	for i, elem := range elems {
		if elem != "--json" {
			continue
		}

		if i+1 == len(elems) {
			out = append(out, where+": --json has no value")

			continue
		}

		out = append(out, fieldFindings(where, shape, allowed, elems[i+1:i+2])...)
	}

	return out
}

// fieldFindings checks each name in the elements: an element is a comma list, or several of them joined by the alternative mark.
func fieldFindings(where, shape string, allowed, elems []string) []string {
	var out []string

	for _, elem := range elems {
		if strings.Contains(elem, unresolved) {
			out = append(out, where+": cannot read the field list from the source: write it as a literal or a constant")

			continue
		}

		out = append(out, nameFindings(where, shape, allowed, strings.FieldsFunc(elem, func(r rune) bool { return r == ',' || string(r) == alternative }))...)
	}

	return out
}

func nameFindings(where, shape string, allowed, fields []string) []string {
	var out []string

	for _, field := range fields {
		switch {
		case !slices.Contains(allowed, field):
			out = append(out, fmt.Sprintf("%s: %q is not a field of gh %s", where, field, shape))
		}
	}

	return out
}

func relative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}

	return rel
}

func TestCallers_EveryJSONFieldIsAFieldOfGh(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err)

	findings, calls := scan(t, root)

	assert.Empty(t, findings, "a field list that gh refuses, or one the scan cannot read")
	assert.GreaterOrEqual(t, calls, 40, "the scan read too few calls of PRView and PRList: it is broken")
}

const planted = `package planted

import "strings"

const worse = "number,authorAssociation"

const split = "number,title"

func direct(f forge)       { f.PRView(ctx, 1, "number", "authorAssociation") }
func viaConst(f forge)     { f.PRList(ctx, 5, "--json", worse) }
func unreadable(f forge, v string) { f.PRList(ctx, 5, "--json", v) }
func noValue(f forge)      { f.PRList(ctx, 5, "--state", "open", "--json") }

func fine(f forge) {
	args := []string{"--state", "open"}
	args = append(args, "--json", "number,title")
	f.PRList(ctx, 5, args...)
	f.PRView(ctx, 1, strings.Split(split, ",")...)
	f.PRView(ctx, 1, listed()...)
	f.PRView(ctx, 1, "number")
	f.PRList(ctx, 5, "--json", "number")
}

func listed() []string { return []string{"state", "isDraft"} }

func helper(f forge, list string) { f.PRList(ctx, 5, "--json", list) }

func caller(f forge) {
	helper(f, "number")
	helper(f, "nope")
}
`

func TestCallers_FailOnAFieldGhRefusesAndOnAListItCannotRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "planted.go"), []byte(planted), 0o600))

	findings, calls := scan(t, root)

	assert.Equal(t, 10, calls)
	assert.Equal(t, []string{
		`planted.go:10: "authorAssociation" is not a field of gh pr list`,
		`planted.go:11: cannot read the field list from the source: write it as a literal or a constant`,
		`planted.go:12: --json has no value`,
		`planted.go:26: "nope" is not a field of gh pr list`,
		`planted.go:9: "authorAssociation" is not a field of gh pr view`,
	}, findings)
}
