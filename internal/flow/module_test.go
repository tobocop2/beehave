package flow

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/step"
	"github.com/tobocop2/beehave/internal/workflow"
)

// shapeOf renders the keywords of a signature for a comparison. A Rule is a function, so the shape holds only whether the keyword has one.
func shapeOf(params []workflow.Param) []string {
	shape := make([]string, 0, len(params))

	for _, p := range params {
		shape = append(shape, fmt.Sprintf("%s %s required=%t values=%v each=%v rule=%t why=%q value=%t", p.Name, p.Kind, p.Required, p.Values, p.Each, p.Rule != nil, p.Why, p.AsValue))
	}

	return shape
}

func TestStepModule_NamesTheBuiltinsOfTheWorkflowPackage(t *testing.T) {
	t.Parallel()

	names := moduleRegistry(t).Names()

	assert.ElementsMatch(t, workflow.Builtins(), append(names, jsonModule))
}

func TestStepModule_HoldsTheSignaturesOfTheWorkflowPackage(t *testing.T) {
	t.Parallel()

	signatures := map[string][]workflow.Param{}

	for _, b := range StepModule().Builtins {
		if b.Params != nil {
			signatures[b.Name] = b.Params
		}
	}

	assert.ElementsMatch(t, workflow.Signed(), mapKeys(signatures))

	for _, name := range workflow.Signed() {
		want, _ := workflow.Signature(name)

		assert.Equal(t, shapeOf(want), shapeOf(signatures[name]), name)
	}
}

func TestStepModule_HoldsTheResultFieldsOfTheWorkflowPackage(t *testing.T) {
	t.Parallel()

	results := map[string][]string{}

	for _, b := range StepModule().Builtins {
		if b.Results != nil {
			results[b.Name] = b.Results
		}
	}

	assert.ElementsMatch(t, workflow.Results(), mapKeys(results))

	for _, name := range workflow.Results() {
		want, _ := workflow.ResultFields(name)

		assert.Equal(t, want, results[name], name)
	}
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	return keys
}

func TestStepModule_GuardsTheBuiltinsThatReadGitOrAForge(t *testing.T) {
	t.Parallel()

	guarded := map[string]string{}
	registry := moduleRegistry(t)

	for _, name := range registry.Names() {
		def, _ := registry.Builtin(name)
		if def.NeedsRepository {
			guarded[name] = def.ExemptKeyword
		}
	}

	assert.Equal(t, map[string]string{
		"base_tip": "", "board_card": "", "gate": "run", "hand_off": "", "integration_pull": "", "issue_assignees": "", "issue_state": "", "landing_proof": "", "merge": "", "pr_closing": "", "pr_comment": "", "pr_history": "", "pr_open": "", "pr_read": "", "pr_state": "", "pr_take": "", "preflight": "", "push": "",
		"round_cut": "", "round_land": "", "round_sync": "",
	}, guarded)
}

func TestNewEngine_RefusesTwoModulesThatNameTheSameBuiltin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Modules = []step.Module{StepModule(), {Name: "extra", Builtins: []step.Builtin{{Name: "now"}}}}

	_, err := NewEngine(t.Context(), h.opts, h.deps())

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "flow" and module "extra" both define the builtin "now"`)
}

func TestNewEngine_RefusesTwoModulesThatRecordTheSameStepKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Modules = []step.Module{StepModule(), {Name: "extra", Kinds: []string{"command"}}}

	_, err := NewEngine(t.Context(), h.opts, h.deps())

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "flow" and module "extra" both record the step kind "command"`)
}

func TestNewEngine_RefusesAnEmptyModuleList(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Modules = nil

	_, err := NewEngine(t.Context(), h.opts, h.deps())

	require.ErrorContains(t, err, "the composition root passed no step modules")
}

func TestNewEngine_TheSameModuleTwiceIsAClash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Modules = []step.Module{StepModule(), StepModule()}

	_, err := NewEngine(t.Context(), h.opts, h.deps())

	require.ErrorContains(t, err, "the step modules clash")
}

func TestRunStar_ABuiltinOfAnotherModuleIsCalledThroughTheRegistry(t *testing.T) {
	t.Parallel()

	var got step.Call

	extra := step.Module{Name: "extra", Builtins: []step.Builtin{{
		Name: "extra_step",
		Call: func(c step.Call, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			got = c

			return starlark.String("from extra"), nil
		},
	}}}

	h := newHarness(t)
	h.opts.Modules = []step.Module{StepModule(), extra}
	engine := idleEngine(t, h)
	s := &script{e: engine, ctx: engine.ctx, in: ItemInput{Project: testProject, Item: testItem}, counts: map[string]int{}}

	value, err := starlark.Call(&starlark.Thread{}, s.predeclared()["extra_step"], nil, nil)

	require.NoError(t, err)
	assert.Equal(t, starlark.String("from extra"), value)
	require.NotNil(t, got)
	assert.Equal(t, testItem, got.Item())
}

func TestRunStar_TheRegistryGuardsABuiltinOfAnotherModuleThatNeedsARepository(t *testing.T) {
	t.Parallel()

	called := false

	extra := step.Module{Name: "extra", Builtins: []step.Builtin{{
		Name: "extra_step", NeedsRepository: true,
		Call: func(step.Call, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			called = true

			return starlark.None, nil
		},
	}}}

	h := newHarness(t)
	h.opts.Modules = []step.Module{StepModule(), extra}
	engine := idleEngine(t, h)
	engine.deps.Repository = func(string) bool { return false }
	s := &script{e: engine, ctx: engine.ctx, in: ItemInput{Project: testProject, Item: testItem}, counts: map[string]int{}}

	_, err := starlark.Call(&starlark.Thread{}, s.predeclared()["extra_step"], nil, nil)

	require.ErrorContains(t, err, "extra_step needs a repository")
	assert.False(t, called)
}

func TestRunStar_ABuiltinWithNoModuleIsUndefined(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Modules = []step.Module{{Name: "other", Builtins: []step.Builtin{{Name: "unrelated"}}}}

	_, err := walkText(t, h, "def main(item):\n    now()\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "now")
}

func TestScripted_RefusesACallThatIsNotTheScriptOfARun(t *testing.T) {
	t.Parallel()

	called := false
	wrapped := scripted(func(*script, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		called = true

		return starlark.None, nil
	})

	_, err := wrapped(otherCall{}, starlark.NewBuiltin("now", nil), nil, nil)

	require.ErrorContains(t, err, "now runs only in a run of the engine that holds its module")
	assert.False(t, called)
}

type otherCall struct{}

func (otherCall) Project() model.Project { return model.Project{} }
func (otherCall) Item() string           { return "" }

func TestScript_ProjectAndItemAreThoseOfTheRun(t *testing.T) {
	t.Parallel()

	want := model.Project{Repo: "acme/app"}
	s := &script{e: &Engine{deps: Deps{Facts: func(name string) model.Project {
		if name == testProject {
			return want
		}

		return model.Project{}
	}}}, in: ItemInput{Project: testProject, Item: testItem}}

	assert.Equal(t, want, s.Project())
	assert.Equal(t, testItem, s.Item())
	assert.Equal(t, model.Project{}, (&script{e: &Engine{}}).Project(), "no Facts means no facts")
}

func TestStepModule_NamesNoKindTwice(t *testing.T) {
	t.Parallel()

	kinds := StepModule().Kinds

	sorted := slices.Clone(kinds)
	slices.Sort(sorted)

	assert.Equal(t, sorted, slices.Compact(slices.Clone(sorted)))
}
