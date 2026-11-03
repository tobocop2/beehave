package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/sandbox"
)

// noEnforcer is the refusal of a machine where no enforcer can run: every gate that the sandbox covers is refused.
func noEnforcer(project config.Project, program bool) string {
	if config.GateRefused(project.Project, program) {
		return config.GateSandboxRefusal
	}

	return ""
}

// fakeEnforcer records the plan and the command of one run, and prints a line.
type fakeEnforcer struct {
	plan sandbox.Plan
	port int
	argv []string
	dir  string
	code int
	// during runs in the command, with its work folder.
	during func(dir string)
}

func (*fakeEnforcer) Name() string { return "fake" }
func (*fakeEnforcer) Check() error { return nil }

func (f *fakeEnforcer) Run(_ context.Context, plan sandbox.Plan, port int, cmd sandbox.Command) (int, error) {
	f.plan, f.port, f.argv, f.dir = plan, port, cmd.Argv, cmd.Dir
	if f.during != nil {
		f.during(cmd.Dir)
	}

	if _, err := cmd.Stdout.Write([]byte("ran\n")); err != nil {
		return 0, err
	}

	return f.code, nil
}

func sandboxFor(t *testing.T, e sandbox.Enforcer, err error) gateSandbox {
	t.Helper()

	home := t.TempDir()
	projects := sandboxedProject(model.SandboxOn, config.LocalHost)

	return gateSandbox{projects: projects, enforcer: func(string) (sandbox.Enforcer, error) { return e, err }, home: func() (string, error) { return home, nil }}
}

func TestGateSandbox_RunsTheCommandUnderTheEnforcerAndRecordsThePlanFirst(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{code: 3}
	dir := t.TempDir()

	out, code := sandboxFor(t, fake, nil).run(t.Context(), "p", dir, []string{"sh", "-c", "make check"}, []string{"A=1"}, 0)

	assert.Equal(t, 3, code, "the exit code passes through")
	assert.Equal(t, []string{"sh", "-c", "make check"}, fake.argv)
	assert.Contains(t, out, "sandbox fake plan: {")
	assert.Contains(t, out, "ran\n")
	assert.Contains(t, fake.plan.Write, dir, "the gate writes the work folder")
	assert.Contains(t, fake.plan.Read, dir)
	assert.Equal(t, []string{"proxy.golang.org"}, fake.plan.Domains)
	assert.True(t, fake.plan.Network)
	assert.NotZero(t, fake.port, "a plan with network gets the port of the proxy")
}

func TestGateSandbox_NoEnforcerGivesTheReasonAndRunsNoCode(t *testing.T) {
	t.Parallel()

	out, code := sandboxFor(t, nil, errors.New("not here")).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "obee sandbox: not here")
}

func TestGateSandbox_ASandboxReadPathThatHoldsTheHomeFolderIsRefused(t *testing.T) {
	t.Parallel()

	box := sandboxFor(t, &fakeEnforcer{}, nil)
	home, err := box.home()
	require.NoError(t, err)

	found := box.projects["p"]
	found.SandboxRead = []string{home}
	box.projects["p"] = found

	out, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "holds the home folder")
}

func TestGateSandbox_AppliesOnlyToAProjectWithTheSandboxOnThatBuildsHere(t *testing.T) {
	t.Parallel()

	assert.True(t, newGateSandbox(sandboxedProject(model.SandboxOn, config.LocalHost)).applies("p", false))
	assert.False(t, newGateSandbox(sandboxedProject(model.SandboxOn, "cluster")).applies("p", false))
	assert.True(t, newGateSandbox(sandboxedProject(model.SandboxOn, "cluster")).applies("p", true))
	assert.False(t, newGateSandbox(sandboxedProject(model.SandboxOff, config.LocalHost)).applies("p", true))
}
