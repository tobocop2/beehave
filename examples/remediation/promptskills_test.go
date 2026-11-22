package remediation_test

import (
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"
	"text/template/parse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillFunction is the template function that declares a skill of a prompt.
const skillFunction = "skill"

// reviewSkillArgument is how a declaration writes the argument that the project vars give: the field chain of the facts.
const reviewSkillArgument = ".facts.vars.review_skill"

// skillFuncs stands in for the skill function of the engine in the tests that render a prompt: it renders the plain name.
var skillFuncs = template.FuncMap{skillFunction: func(name string) string { return name }}

// promptFiles returns the text of every prompt of the example by file name.
func promptFiles(t *testing.T) map[string]string {
	t.Helper()

	entries, err := fs.ReadDir(os.DirFS("prompts"), ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	files := map[string]string{}

	for _, entry := range entries {
		raw, err := fs.ReadFile(os.DirFS("prompts"), entry.Name())
		require.NoError(t, err)

		files[entry.Name()] = string(raw)
	}

	return files
}

// namedSkills are the skills that any prompt of the example declares by name. A backticked name of one outside the template function, in any prompt,
// is a skill the step does not get.
func namedSkills(t *testing.T) []string {
	t.Helper()

	var names []string

	for _, text := range promptFiles(t) {
		var f skillFinds

		for _, tree := range parseTemplate(t, text) {
			walk(tree.Root, f.visit)
		}

		for _, name := range f.declared {
			if !strings.HasPrefix(name, ".") && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

// walk calls visit for the node and for every node below it.
func walk(node parse.Node, visit func(parse.Node)) {
	visit(node)

	for _, child := range childNodes(node) {
		if child != nil {
			walk(child, visit)
		}
	}
}

// declaredSkill returns the argument of a call of the skill function: the text of a string argument, or the field chain of a field argument.
func declaredSkill(command *parse.CommandNode) (string, bool) {
	if len(command.Args) != 2 {
		return "", false
	}

	if name, ok := command.Args[0].(*parse.IdentifierNode); !ok || name.Ident != skillFunction {
		return "", false
	}

	switch arg := command.Args[1].(type) {
	case *parse.StringNode:
		return arg.Text, true
	case *parse.FieldNode:
		return "." + strings.Join(arg.Ident, "."), true
	}

	return "", false
}

// skillsOf walks the parse tree of a prompt. It returns the skills that the prompt declares through the function, in order, and the known skills that the text
// of the prompt names in backticks outside the function.
func skillsOf(t *testing.T, text string) (declared, stray []string) {
	t.Helper()

	f := skillFinds{known: namedSkills(t)}

	for _, tree := range parseTemplate(t, text) {
		walk(tree.Root, f.visit)
	}

	return f.declared, f.stray
}

// skillFinds are the skills that the walk of a prompt found. known is the set of skill names that a backticked name can match.
type skillFinds struct {
	declared, stray, known []string
}

var backticked = regexp.MustCompile("`([A-Za-z0-9][A-Za-z0-9._-]*)`")

// visit reads one node: a call of the skill function is a declaration, and a known skill in the backticks of a text is a stray name.
func (f *skillFinds) visit(node parse.Node) {
	switch n := node.(type) {
	case *parse.CommandNode:
		if name, ok := declaredSkill(n); ok && !slices.Contains(f.declared, name) {
			f.declared = append(f.declared, name)
		}
	case *parse.TextNode:
		f.strayIn(string(n.Text))
	}
}

func (f *skillFinds) strayIn(text string) {
	for _, found := range backticked.FindAllStringSubmatch(text, -1) {
		if slices.Contains(f.known, found[1]) {
			f.stray = append(f.stray, found[1])
		}
	}
}

func TestPrompts_EverySkillThatAPromptNamesIsDeclared(t *testing.T) {
	t.Parallel()

	declared, stray := skillsOf(t, "Run `{{ skill \"blast-radius-review\" }}`, not `plan-vs-conventions`. Use {{ skill .facts.vars.review_skill }}.")
	require.Equal(t, []string{"blast-radius-review", reviewSkillArgument}, declared, "the walk reads a string argument and a field argument")
	require.Equal(t, []string{"plan-vs-conventions"}, stray, "the walk finds a backticked name outside the function, and not one inside it")

	_, stray = skillsOf(t, "{{ if .x }}Run `cross-cutting-api-change`.{{ end }}")
	require.Equal(t, []string{"cross-cutting-api-change"}, stray, "the walk reads the body of an if")

	floor := []string{reviewSkillArgument}
	work := []string{"plan-vs-conventions", "cross-cutting-api-change", reviewSkillArgument}

	wants := map[string][]string{plan + ".md": floor, review + ".md": floor, implement + ".md": work, fix + ".md": work}
	files := promptFiles(t)

	require.Contains(t, namedSkills(t), "plan-vs-conventions", "the known names come from the declarations of every prompt")

	for name, text := range files {
		got, stray := skillsOf(t, text)

		assert.Empty(t, stray, "prompts/%s names a skill outside the function, so the step is not given it", name)

		if want, ordered := wants[name]; ordered {
			assert.Equal(t, want, got, "prompts/%s declares the skills that it orders", name)
		} else {
			assert.Empty(t, got, "prompts/%s declares a skill that this test does not list", name)
		}
	}
}

func TestPrompts_NoPromptTellsTheAgentWhatToDoWhenTheSkillIsUnknown(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(os.DirFS("prompts"), ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		raw, err := fs.ReadFile(os.DirFS("prompts"), entry.Name())
		require.NoError(t, err)

		assert.NotContains(t, strings.ToLower(squash(string(raw))), "unknown skill", "prompts/%s keeps the fallback for a skill that the step does not get", entry.Name())
	}
}
