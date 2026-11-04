package flow

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgepkg "github.com/tobocop2/obee/internal/forge"
	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/host"
)

var errNotKept = errors.New("an error that nobody keeps")

func TestFailure_AKnownErrorInTheChainIsFoundAgainAfterTheRebuild(t *testing.T) {
	t.Parallel()

	engine := &Engine{deps: Deps{Kept: []error{errFake}}}
	original := fmt.Errorf("gate \"check\": %w", errors.Join(gatepkg.ErrNoVerdict, errFake))

	failure := engine.failureOf(original)
	rebuilt := failure.err(engine.knownErrors())

	require.ErrorIs(t, rebuilt, gatepkg.ErrNoVerdict)
	require.ErrorIs(t, rebuilt, errFake)
	require.NotErrorIs(t, rebuilt, gatepkg.ErrNoTests)
	assert.Equal(t, original.Error(), rebuilt.Error())
}

func TestFailure_AnErrorThatNobodyKeepsKeepsItsTextOnly(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	rebuilt := engine.failureOf(errNotKept).err(engine.knownErrors())

	require.NotErrorIs(t, rebuilt, errNotKept)
	require.EqualError(t, rebuilt, errNotKept.Error())
}

// sentinelNames returns the names of the exported Err variables that the non-test files of the folders declare.
func sentinelNames(t *testing.T, folders ...string) map[string]string {
	t.Helper()

	found := map[string]string{}

	for _, folder := range folders {
		files, err := filepath.Glob(filepath.Join(folder, "*.go"))
		require.NoError(t, err)

		for _, name := range files {
			if !strings.HasSuffix(name, "_test.go") {
				addSentinels(t, name, found)
			}
		}
	}

	return found
}

// addSentinels adds the exported Err names that the file declares to found.
func addSentinels(t *testing.T, name string, found map[string]string) {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	require.NoError(t, err)

	ast.Inspect(file, func(n ast.Node) bool {
		if spec, ok := n.(*ast.ValueSpec); ok {
			for _, id := range spec.Names {
				if strings.HasPrefix(id.Name, "Err") {
					found[id.Name] = name
				}
			}
		}

		return true
	})
}

// listedNames returns the names of the Err values in the body of productionKnown.
func listedNames(t *testing.T) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "errkind.go", nil, 0)
	require.NoError(t, err)

	listed := map[string]bool{}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "productionKnown" {
			continue
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				listed[sel.Sel.Name] = true
			}

			if id, ok := n.(*ast.Ident); ok {
				listed[id.Name] = true
			}

			return true
		})
	}

	return listed
}

func TestKnownErrors_EachExportedSentinelIsListed(t *testing.T) {
	t.Parallel()

	declared := sentinelNames(t, "../gate", "../host", "../forge", "../merge", "../batch")
	require.NotEmpty(t, declared)

	listed := listedNames(t)

	for name, where := range declared {
		assert.True(t, listed[name], "%s (declared in %s) is not in productionKnown: add it, or the error loses its identity across a gate or a merge", name, where)
	}
}

func TestKnownErrors_TheProductionListHoldsTheContextAndFileErrors(t *testing.T) {
	t.Parallel()

	engine := &Engine{}

	for _, want := range []error{context.Canceled, context.DeadlineExceeded, os.ErrNotExist, forgepkg.ErrNoForge, host.ErrNoRepos, ErrMemberNotOpen} {
		assert.Contains(t, engine.knownErrors(), want)
	}
}

func TestKnownErrors_AKeptErrorAddsToTheProductionListAndNeverReplacesIt(t *testing.T) {
	t.Parallel()

	engine := &Engine{deps: Deps{Kept: []error{errFake}}}

	assert.Contains(t, engine.knownErrors(), errFake)
	assert.Contains(t, engine.knownErrors(), context.DeadlineExceeded)
}

type deadlineGate struct{}

// Run hits a real deadline of its own and returns the error of it.
func (deadlineGate) Submit(context.Context, string, string, string) (GateJob, error) {
	return GateJob{}, nil
}

func (deadlineGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	limited, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()

	<-limited.Done()

	return GateResult{}, fmt.Errorf("the gate ran out of time: %w", limited.Err())
}

func TestFailure_AGateThatHitsItsDeadlineGivesTheDeadlineErrorToTheParentWithNoKeptList(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.deps()
	deps.Gate, deps.Kept = deadlineGate{}, nil
	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestFailure_NoErrorGivesNoFailureAndNoError(t *testing.T) {
	t.Parallel()

	engine := &Engine{}

	assert.Nil(t, engine.failureOf(nil))
	assert.NoError(t, engine.failureOf(nil).err(engine.knownErrors()))
}
