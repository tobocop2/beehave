package enforcers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/sandbox"
)

type stub struct{ err error }

func (stub) Name() string { return "stub" }
func (s stub) Check() error {
	return s.err
}
func (stub) Run(context.Context, sandbox.Plan, int, sandbox.Command) (int, error) { return 0, nil }

func sandboxedProject(mode model.SandboxMode, enforcer string) config.Project {
	return config.Project{Project: model.Project{Sandbox: mode, BuildHost: config.LocalHost, SandboxEnforcer: enforcer}}
}

func TestRefusal_AGateUnderTheSandboxIsRefusedWhenNoEnforcerCanRunAndTheReasonEndsTheText(t *testing.T) {
	t.Parallel()

	down := map[string]sandbox.Enforcer{"stub": stub{err: errors.Join(sandbox.ErrUnavailable, errors.New("the kernel is too old"))}}
	up := map[string]sandbox.Enforcer{"stub": stub{}}
	order := []string{"stub"}

	got := refusal(sandboxedProject(model.SandboxOn, ""), false, down, order)
	assert.Contains(t, got, config.GateSandboxRefusal)
	assert.Contains(t, got, "the kernel is too old")

	assert.Empty(t, refusal(sandboxedProject(model.SandboxOn, ""), false, up, order), "an enforcer that can run lets the gate run")
	assert.Empty(t, refusal(sandboxedProject(model.SandboxOff, ""), false, down, order), "a project with the sandbox off is never refused")
	assert.Contains(t, refusal(sandboxedProject(model.SandboxOn, "nope"), false, up, order), `"nope"`, "a name that is not in the table is refused with the names")
}

func TestRefusal_TheRealTableRefusesANameItDoesNotHoldAndNeverRefusesAProjectWithTheSandboxOff(t *testing.T) {
	t.Parallel()

	got := Refusal(sandboxedProject(model.SandboxOn, "nope"), false)
	assert.Contains(t, got, config.GateSandboxRefusal)
	assert.Contains(t, got, `"nope"`)

	assert.Empty(t, Refusal(sandboxedProject(model.SandboxOff, "nope"), false))
}

func TestOrder_NamesEveryEnforcerOfTheTable(t *testing.T) {
	t.Parallel()

	table := Table()
	for _, name := range Order() {
		assert.Contains(t, table, name)
	}

	assert.Len(t, table, len(Order()))
}
