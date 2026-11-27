package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
)

// offProject is a project whose sandbox key is off: only a command call that sets the sandbox keyword runs under the enforcer.
func offProject(t *testing.T, fake *fakeEnforcer) gateSandbox {
	t.Helper()

	box := sandboxFor(t, fake, nil)
	box.projects = sandboxedProject(model.SandboxOff, config.LocalHost)

	return box
}

func TestPrograms_ACommandWithTheSandboxKeywordRunsUnderTheEnforcerWhateverTheProjectSays(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{code: 6}
	dir := t.TempDir()
	runs := programs{trees: treeAt(dir), sandbox: offProject(t, fake)}

	got, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"go", "env", "GOCACHE"}, Sandbox: &model.Sandbox{}})

	require.NoError(t, err)
	assert.Equal(t, 6, got.RC)
	assert.Equal(t, []string{"go", "env", "GOCACHE"}, fake.argv)
	assert.Contains(t, got.Stdout, "sandbox fake plan: {")
	assert.False(t, fake.plan.Network, "a sandbox with no list has no network, though the project lists a gate_network")
	assert.Empty(t, fake.plan.Domains)
}

func TestPrograms_ACommandWithTheSandboxKeywordAndNoItemFolderRunsInAnEmptyFolderOfItsOwn(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	runs := programs{trees: treeAt(""), sandbox: offProject(t, fake)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Sandbox: &model.Sandbox{}})

	require.NoError(t, err)
	assert.NotEmpty(t, fake.dir)
	assert.Contains(t, fake.plan.Write, fake.dir, "the empty folder is made for the command alone, so it may write it")
}

func TestPrograms_ACommandWithNoSandboxKeywordRunsOutsideTheEnforcerInAProjectWithTheSandboxOff(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	dir := t.TempDir()

	var spec proc.ExecRunner

	runs := programs{
		trees: treeAt(dir), sandbox: offProject(t, fake),
		exec: func(s proc.ExecRunner) proc.Runner {
			spec = s

			return s
		},
	}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Timeout: time.Minute})

	require.NoError(t, err)
	assert.Nil(t, fake.argv, "the enforcer ran nothing")
	assert.Equal(t, "true", spec.Binary)
}

func TestGateSandbox_AskedGivesACommandItsOwnNetworkListAndLeavesTheListsOfTheProjectAlone(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)

	_, code := box.asked("p", &model.Sandbox{Network: []string{"example.org"}}).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	require.Zero(t, code)
	assert.Equal(t, []string{"example.org"}, fake.plan.Domains, "the list of the call replaces gate_network")
	assert.Equal(t, []string{"proxy.golang.org"}, box.projects["p"].GateNetwork, "the project of the engine keeps its list")
	assert.Equal(t, []string{"cache"}, box.projects["p"].GateAllowWrite)

	fake = &fakeEnforcer{}
	box = sandboxFor(t, fake, nil)

	_, code = box.asked("p", nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	require.Zero(t, code)
	assert.Equal(t, []string{"proxy.golang.org"}, fake.plan.Domains, "a gate keeps the list of the project")
}

func TestGateSandbox_ACommandGetsNoConfigFileOfTheOwnerAndAGateIsLeftAsItWas(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)

	_, code := box.asked("p", &model.Sandbox{}).run(t.Context(), "p", t.TempDir(), []string{"true"}, []string{"HOME=/home/me"}, 0)

	require.Zero(t, code)
	assert.Contains(t, fake.env, "GIT_CONFIG_GLOBAL=/dev/null", "the plan closes ~/.gitconfig")
	assert.Contains(t, fake.env, "GIT_CONFIG_NOSYSTEM=1")
	assert.Contains(t, fake.env, "HOME=/home/me", "go and bd keep the home folder that they start from")

	fake = &fakeEnforcer{}
	box = sandboxFor(t, fake, nil)

	_, code = box.run(t.Context(), "p", t.TempDir(), []string{"true"}, []string{"HOME=/home/me"}, 0)

	require.Zero(t, code)
	assert.NotContains(t, strings.Join(fake.env, "\n"), "GIT_CONFIG", "the gate environment is not changed")
}

func TestGitVariables_CarryTheIdentityAsConfigValuesAndLeaveOutAnEmptyOne(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		identity agent.GitIdentity
		want     []string
	}{
		"both": {
			agent.GitIdentity{Name: `A "B" \ C`, Email: "a@example.org"},
			[]string{
				"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=2",
				`GIT_CONFIG_KEY_0=user.name`, `GIT_CONFIG_VALUE_0=A "B" \ C`, "GIT_CONFIG_KEY_1=user.email", "GIT_CONFIG_VALUE_1=a@example.org",
			},
		},
		"email only": {
			agent.GitIdentity{Email: "a@example.org"},
			[]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.email", "GIT_CONFIG_VALUE_0=a@example.org"},
		},
		"none": {
			agent.GitIdentity{},
			[]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, gitVariables(tc.identity))
		})
	}
}
