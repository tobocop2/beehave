package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/sandbox"
)

// envEnforcer keeps the environment of the command it ran.
type envEnforcer struct{ env []string }

func (*envEnforcer) Name() string { return "env" }
func (*envEnforcer) Check() error { return nil }
func (e *envEnforcer) Run(_ context.Context, _ sandbox.Plan, _ int, cmd sandbox.Command) (int, error) {
	e.env = cmd.Env

	return 0, nil
}

// stuckEnforcer runs until its context ends, as a command that outlasts its limit.
type stuckEnforcer struct{}

func (stuckEnforcer) Name() string { return "stuck" }
func (stuckEnforcer) Check() error { return nil }
func (stuckEnforcer) Run(ctx context.Context, _ sandbox.Plan, _ int, _ sandbox.Command) (int, error) {
	<-ctx.Done()

	return 0, ctx.Err()
}

// brokenEnforcer cannot run the command.
type brokenEnforcer struct{}

func (brokenEnforcer) Name() string { return "broken" }
func (brokenEnforcer) Check() error { return nil }
func (brokenEnforcer) Run(context.Context, sandbox.Plan, int, sandbox.Command) (int, error) {
	return 0, errors.New("the helper did not start")
}

func TestNewGateSandbox_ANameThatNoEnforcerHasIsRefusedWithTheNames(t *testing.T) {
	t.Parallel()

	box := newGateSandbox(sandboxedProject(model.SandboxOn, config.LocalHost))

	_, err := box.enforcer("nope")
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	require.ErrorContains(t, err, `"nope"`)

	found, err := box.enforcer("auto")
	if err != nil {
		require.ErrorIs(t, err, sandbox.ErrUnavailable, "a machine without an enforcer refuses with the reason")

		return
	}

	// auto picks the enforcer of the system: Landlock on Linux, Seatbelt on macOS.
	assert.Equal(t, map[string]string{"linux": "landlock", "darwin": "seatbelt"}[runtime.GOOS], found.Name())
}

func TestGateSandbox_ACommandWithNoEnvironmentGetsTheOneTheSandboxWasMadeWithAndItsOwnTemporaryFolder(t *testing.T) {
	t.Parallel()

	fake := &envEnforcer{}
	box := sandboxFor(t, fake, nil)
	box.environ = []string{"E=1"}

	_, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)
	require.Zero(t, code)
	assert.Contains(t, fake.env, "E=1")
	assert.Condition(t, func() bool {
		for _, pair := range fake.env {
			if len(pair) > len("TMPDIR=") && pair[:len("TMPDIR=")] == "TMPDIR=" {
				return true
			}
		}

		return false
	}, "the command gets a TMPDIR of its own")

	_, code = box.run(t.Context(), "p", t.TempDir(), []string{"true"}, []string{"ONLY=1"}, 0)
	require.Zero(t, code)
	assert.Contains(t, fake.env, "ONLY=1")
	assert.NotContains(t, fake.env, "E=1", "an environment in the request replaces the default")
}

func TestGateSandbox_ACommandThatOutlastsItsLimitEndsWithTheTimeoutCode(t *testing.T) {
	t.Parallel()

	out, code := sandboxFor(t, stuckEnforcer{}, nil).run(t.Context(), "p", t.TempDir(), []string{"sleep", "99"}, nil, 50*time.Millisecond)

	assert.Equal(t, gate.ExitTimedOut, code)
	assert.Contains(t, out, "sandbox stuck plan: {", "the plan is recorded even when the command times out")
}

func TestGateSandbox_AnEnforcerThatCannotRunTheCommandGivesTheReasonAfterThePlan(t *testing.T) {
	t.Parallel()

	out, code := sandboxFor(t, brokenEnforcer{}, nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "sandbox broken plan: {")
	assert.Contains(t, out, "obee sandbox: the helper did not start")
}

func TestGateSandbox_APlanThatCannotBeMadeRunsNoCommand(t *testing.T) {
	t.Parallel()

	noHome := sandboxFor(t, &envEnforcer{}, nil)
	noHome.home = func() (string, error) { return "", errors.New("no home") }

	out, code := noHome.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "cannot find the home folder")

	box := sandboxFor(t, &envEnforcer{}, nil)
	home, err := box.home()
	require.NoError(t, err)

	found := box.projects["p"]
	found.GateAllowWrite = []string{filepath.Join(home, ".ssh")}
	box.projects["p"] = found

	out, code = box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "overlaps the protected path")
	assert.Contains(t, out, "The gate did not run.")
	assert.NotContains(t, out, "The step did not start.")
}

func TestGateSandbox_ATemporaryFolderThatCannotBeMadeRunsNoCommand(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))

	out, code := sandboxFor(t, &envEnforcer{}, nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "make the temporary folder")
}

func TestPrograms_AGateProgramOfASandboxedProjectRunsUnderTheEnforcerAndAnotherProgramDoesNot(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{code: 4}
	dir := t.TempDir()
	runs := programs{trees: treeAt(dir), sandbox: sandboxFor(t, fake, nil)}

	got, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"make", "check"}, Gate: true})
	require.NoError(t, err)
	assert.Equal(t, 4, got.RC)
	assert.Contains(t, got.Stdout, "sandbox fake plan: {")
	assert.Equal(t, []string{"make", "check"}, fake.argv)
	assert.Contains(t, fake.plan.Read, dir)
}

func TestPrograms_AGateProgramWhoseFolderCannotBeMadeIsAnError(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))

	runs := programs{trees: treeAt(""), sandbox: sandboxFor(t, &fakeEnforcer{}, nil)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"make"}, Gate: true})
	require.Error(t, err)
}

func TestProjectGate_ASandboxedProjectGateRunsTheCommandUnderTheEnforcer(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{code: 5}
	box := sandboxFor(t, fake, nil)
	gate := projectGate{projects: box.projects, programs: programs{sandbox: box}}

	out, code := gate.shellFor("p")(t.Context(), t.TempDir(), "make check")

	assert.Equal(t, 5, code)
	assert.Contains(t, out, "sandbox fake plan: {")
	assert.Equal(t, []string{"sh", "-c", "make check"}, fake.argv)
}
