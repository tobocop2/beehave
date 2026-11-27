package flow

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow"
)

// The contract test of the workflow API. It reads the builtins and keywords that the engine registers, the target table of docs/design/workflow-api.md,
// and every call in examples/ and in the embedded library. A builtin or keyword with no row, or with no call that uses it, fails the test, and so does a row
// that names no builtin or keyword of the code. The test has no list of exemptions.

// contractRepo is the folder of the repository, seen from the folder of this package.
const contractRepo = "../.."

// contractDoc is the contract of the workflow API.
const contractDoc = "docs/design/workflow-api.md"

// noKeyword is the keyword cell of the row of a builtin itself.
const noKeyword = "-"

// contractPair is one builtin, or one keyword of a builtin when keyword is not noKeyword.
type contractPair struct{ builtin, keyword string }

func (p contractPair) String() string {
	if p.keyword == noKeyword {
		return p.builtin
	}

	return p.builtin + "." + p.keyword
}

// contractCode is the builtin set of the engine: json, and each builtin of the module with each keyword of its signature.
// The order of the keywords of a builtin is the order of their positions.
func contractCode() (pairs map[contractPair]bool, order map[string][]string) {
	pairs = map[contractPair]bool{{"json", noKeyword}: true}
	order = map[string][]string{}

	for _, b := range StepModule().Builtins {
		pairs[contractPair{b.Name, noKeyword}] = true

		for _, p := range b.Params {
			pairs[contractPair{b.Name, p.Name}] = true
			order[b.Name] = append(order[b.Name], p.Name)
		}
	}

	return pairs, order
}

// contractRows reads the rows between the markers of the target table. A row is | `builtin` | `keyword` or - | change | use |.
func contractRows(t *testing.T, path string) map[contractPair]bool {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // the path is the contract of this repository
	require.NoError(t, err, "open the contract")

	defer func() { require.NoError(t, f.Close()) }()

	rows := map[contractPair]bool{}
	inside := false
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.Contains(line, "<!-- contract:target -->"):
			inside = true
		case strings.Contains(line, "<!-- /contract:target -->"):
			inside = false
		case inside && strings.HasPrefix(line, "| `"):
			cells := strings.Split(line, "|")
			rows[contractPair{strings.Trim(strings.TrimSpace(cells[1]), "`"), strings.Trim(strings.TrimSpace(cells[2]), "`")}] = true
		}
	}

	require.NoError(t, scanner.Err())
	require.NotEmpty(t, rows, "the contract has no row between the markers of the target table")

	return rows
}

// contractUses is the pairs that some call uses, with the files of the calls.
type contractUses map[contractPair]map[string]bool

func (u contractUses) add(p contractPair, file string) {
	if u[p] == nil {
		u[p] = map[string]bool{}
	}

	u[p][file] = true
}

// contractFiles lists the Starlark files of examples/ and of the embedded library.
func contractFiles(t *testing.T, repo string) []string {
	t.Helper()

	var files []string

	for _, root := range []string{"examples", filepath.Join("internal", "workflow", "lib")} {
		err := filepath.WalkDir(filepath.Join(repo, root), func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".star") {
				files = append(files, path)
			}

			return err
		})
		require.NoError(t, err, "walk "+root)
	}

	return files
}

// contractDictKeys is the text keys of an expression that builds a dict: a dict literal, a dict(...) call, or a conditional of the two.
func contractDictKeys(e syntax.Expr) []string {
	switch x := e.(type) {
	case *syntax.DictExpr:
		var keys []string

		for _, entry := range x.List {
			if lit, ok := entry.(*syntax.DictEntry).Key.(*syntax.Literal); ok && lit.Token == syntax.STRING {
				keys = append(keys, lit.Value.(string)) //nolint:forcetypeassert,errcheck // a string literal holds a string
			}
		}

		return keys
	case *syntax.CallExpr:
		return dictCallKeys(x)
	case *syntax.CondExpr:
		return append(contractDictKeys(x.True), contractDictKeys(x.False)...)
	case *syntax.ParenExpr:
		return contractDictKeys(x.X)
	}

	return nil
}

// dictCallKeys is the keyword names of a dict(...) call, and none for any other call.
func dictCallKeys(call *syntax.CallExpr) []string {
	fn, ok := call.Fn.(*syntax.Ident)
	if !ok || fn.Name != "dict" {
		return nil
	}

	var keys []string

	for _, arg := range call.Args {
		if bin, isKeyword := arg.(*syntax.BinaryExpr); isKeyword && bin.Op == syntax.EQ {
			keys = append(keys, bin.X.(*syntax.Ident).Name) //nolint:forcetypeassert,errcheck // a keyword argument names an identifier
		}
	}

	return keys
}

// contractName is the name that an expression reads: x for x, b for a.b, and f for f(...).
func contractName(e syntax.Expr) string {
	switch x := e.(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.DotExpr:
		return x.Name.Name
	case *syntax.CallExpr:
		return contractName(x.Fn)
	}

	return ""
}

// contractSplats maps a name to the keys of every dict that one file binds to it: an assignment, a keyword argument, or a return of a def of that name.
// A splat resolves in its own file only, so the dict of another file with the same name adds no key.
func contractSplats(file *syntax.File) map[string][]string {
	out := map[string][]string{}

	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.AssignStmt:
			out[contractName(x.LHS)] = append(out[contractName(x.LHS)], contractDictKeys(x.RHS)...)
		case *syntax.BinaryExpr:
			if x.Op == syntax.EQ {
				out[contractName(x.X)] = append(out[contractName(x.X)], contractDictKeys(x.Y)...)
			}
		case *syntax.DefStmt:
			syntax.Walk(x, func(inner syntax.Node) bool {
				if ret, ok := inner.(*syntax.ReturnStmt); ok && ret.Result != nil {
					out[x.Name.Name] = append(out[x.Name.Name], contractDictKeys(ret.Result)...)
				}

				return true
			})
		}

		return true
	})

	return out
}

// contractWalk records each call of a builtin of the code in the files, with each keyword that the call passes, by name, by position or by a splat.
func contractWalk(t *testing.T, repo string, code map[contractPair]bool, order map[string][]string) contractUses {
	t.Helper()

	uses := contractUses{}

	for _, path := range contractFiles(t, repo) {
		file, err := model.StarlarkFileOptions().Parse(path, nil, 0)
		require.NoError(t, err, "parse "+path)

		rel, err := filepath.Rel(repo, path)
		require.NoError(t, err)

		splats := contractSplats(file)

		syntax.Walk(file, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok {
				contractCall(call, rel, code, order, splats, uses)
			}

			return true
		})
	}

	return uses
}

// contractCall records one call: the builtin, and the keywords that its arguments bind.
func contractCall(call *syntax.CallExpr, file string, code map[contractPair]bool, order map[string][]string, splats map[string][]string, uses contractUses) {
	if dot, isDot := call.Fn.(*syntax.DotExpr); isDot {
		if base, isIdent := dot.X.(*syntax.Ident); isIdent && base.Name == "json" {
			uses.add(contractPair{"json", noKeyword}, file)
		}
	}

	fn, named := call.Fn.(*syntax.Ident)
	if !named || !code[contractPair{fn.Name, noKeyword}] {
		return
	}

	uses.add(contractPair{fn.Name, noKeyword}, file)

	for i, arg := range call.Args {
		for _, keyword := range contractKeywords(arg, i, order[fn.Name], splats) {
			if code[contractPair{fn.Name, keyword}] {
				uses.add(contractPair{fn.Name, keyword}, file)
			}
		}
	}
}

// contractKeywords is the keywords that the argument at position i binds: the keyword it names, the keyword at its position in the signature,
// or the keys of the dict that a splat reads.
func contractKeywords(arg syntax.Expr, i int, order []string, splats map[string][]string) []string {
	switch a := arg.(type) {
	case *syntax.BinaryExpr:
		if a.Op == syntax.EQ {
			return []string{a.X.(*syntax.Ident).Name} //nolint:forcetypeassert,errcheck // a keyword argument names an identifier
		}
	case *syntax.UnaryExpr:
		if a.Op == syntax.STARSTAR {
			return splats[contractName(a.X)]
		}
	default:
		if i < len(order) {
			return []string{order[i]}
		}
	}

	return nil
}

func TestExamples_EveryBuiltinAndKeywordHasAUse(t *testing.T) {
	t.Parallel()

	code, order := contractCode()
	uses := contractWalk(t, contractRepo, code, order)
	rows := contractRows(t, filepath.Join(contractRepo, contractDoc))

	for p := range code {
		assert.True(t, rows[p], "%s: the code has it and %s has no row", p, contractDoc)
		assert.NotEmpty(t, uses[p], "%s: no call in examples/ or the library uses it. Add a use with a test, or remove it", p)
	}

	for p := range rows {
		assert.True(t, code[p], "%s: %s has a row and the code has no such builtin or keyword", p, contractDoc)
	}
}

func TestExamples_TheBuiltinsOfTheRegistryAreTheBuiltinsTheWorkflowFilesSee(t *testing.T) {
	t.Parallel()

	var registered []string

	for _, b := range StepModule().Builtins {
		registered = append(registered, b.Name)
	}

	// json is the module of the Starlark library, so it is no step of the registry.
	registered = append(registered, "json")
	slices.Sort(registered)

	assert.Equal(t, registered, workflow.Builtins())
}

func TestContractSplats_AnAssignmentInAnotherFileAddsNoKey(t *testing.T) {
	t.Parallel()

	parse := func(src string) *syntax.File {
		file, err := model.StarlarkFileOptions().Parse("f.star", src, 0)
		require.NoError(t, err)

		return file
	}

	own := contractSplats(parse("extra = {\"quorum\": 2}\nopts = dict(remind = \"1h\") if x else {\"to\": []}\ndef more():\n    return (dict(name = \"n\"))\n"))
	other := contractSplats(parse("extra = {\"timeout\": \"1h\"}\n"))

	assert.Equal(t, []string{"quorum"}, own["extra"])
	assert.Equal(t, []string{"remind", "to"}, own["opts"])
	assert.Equal(t, []string{"name"}, own["more"])
	assert.Equal(t, []string{"timeout"}, other["extra"])
}

func TestContractCall_OnlyTheExactNameOfABuiltinAndTheKeywordsOfThatCallCount(t *testing.T) {
	t.Parallel()

	code := map[contractPair]bool{{"wait", noKeyword}: true, {"wait", "quorum"}: true, {"wait", "remind"}: true}
	order := map[string][]string{"wait": {"prompt", "quorum", "remind"}}

	uses := func(src string) contractUses {
		file, err := model.StarlarkFileOptions().Parse("f.star", src, 0)
		require.NoError(t, err)

		found := contractUses{}

		syntax.Walk(file, func(node syntax.Node) bool {
			if call, ok := node.(*syntax.CallExpr); ok {
				contractCall(call, "f.star", code, order, contractSplats(file), found)
			}

			return true
		})

		return found
	}

	assert.Empty(t, uses(`waiter(quorum = 2)`), "a name that holds the builtin name is no use")
	assert.Empty(t, uses(`other(quorum = 2, remind = 1)`), "a keyword of another call is no use")
	assert.Len(t, uses("wait(\"p\", 2)\nwait(\"p\", 2)"), 2, "two calls with the same keyword count once")
	assert.Equal(t, uses(`wait("p", remind = 1, quorum = 2)`), uses(`wait("p", quorum = 2, remind = 1)`), "the order of the keywords does not matter")
}
