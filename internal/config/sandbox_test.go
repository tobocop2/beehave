package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

func TestProject_AProjectWithNoSandboxKeyHasTheSandboxOn(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Equal(t, model.SandboxOn, project.Sandbox)
	require.True(t, project.SandboxByDefault())
	require.Equal(t, model.SandboxOn, project.RunSandboxMode(), "a run of the project stores the mode on")
	require.True(t, project.SandboxUnset, "the project records that its file sets no sandbox key")
}

func TestLoadFolder_TheSandboxKeyIsReadAndAnUnknownOrEmptyOneIsRefused(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, sandbox = "on"`))
	require.Empty(t, problems)
	require.Equal(t, model.SandboxOn, project.Sandbox)
	require.True(t, project.SandboxByDefault())
	require.False(t, project.SandboxUnset)

	project, problems = loadProjectText(t, call(goodProject+`, sandbox = "off"`))
	require.Empty(t, problems)
	require.False(t, project.SandboxByDefault())
	require.Equal(t, model.SandboxOff, project.RunSandboxMode())
	require.False(t, project.SandboxUnset)

	_, problems = loadProjectText(t, call(goodProject+`, sandbox = ""`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`sandbox` must be a non-empty string")

	_, problems = loadProjectText(t, call(goodProject+`, sandbox = "yes"`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), `unknown sandbox mode "yes"`)
}

func TestLoadFolder_TheSandboxReadAndDenyListsExtendTheEngineLists(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, sandbox_read = ["/opt/tools", "~/sdk"], sandbox_deny = ["~/.config/vault"]`))
	require.Empty(t, problems)
	require.Equal(t, []string{"/opt/tools", "~/sdk"}, project.SandboxRead)
	require.Equal(t, []string{"~/.config/vault"}, project.SandboxDeny)
}

func TestLoadFolder_TheSandboxEnforcerKeyTakesATextAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, sandbox_enforcer = "landlock"`))
	require.Empty(t, problems)
	require.Equal(t, "landlock", project.SandboxEnforcer)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.SandboxEnforcer, "an unset key means auto")

	_, problems = loadProjectText(t, call(goodProject+`, sandbox_enforcer = ""`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "sandbox_enforcer")
}

func TestLoadFolder_ASandboxReadPathThatOpensTheHomeFolderOrACredentialIsRefused(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"/", "~/", "~/.ssh", "~/.ssh/keys", "/opt/app/.env"} {
		_, problems := loadProjectText(t, call(goodProject+`, sandbox_read = ["`+bad+`"]`))
		require.Len(t, problems, 1, bad)
		require.Contains(t, problems[0].Error(), "sandbox_read lists", bad)
	}
}

func TestLoadFolder_ARelativeSandboxPathOrAListOfOtherThingsIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(goodProject+`, sandbox_read = ["tools"]`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`sandbox_read` lists \"tools\". Write an absolute path, or one that starts with ~/.")

	_, problems = loadProjectText(t, call(goodProject+`, sandbox_deny = ["rel/x"]`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`sandbox_deny` lists \"rel/x\"")

	_, problems = loadProjectText(t, call(goodProject+`, sandbox_read = "/opt"`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "want a list of strings")

	_, problems = loadProjectText(t, call(goodProject+`, sandbox_read = [1]`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "want a list of strings")
}

func TestLoadFolder_TheGateGetsItsOwnNetworkAndWriteLists(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, gate_network = ["proxy.golang.org"], gate_allow_write = ["~/go/pkg", "out"]`))
	require.Empty(t, problems)
	require.Equal(t, []string{"proxy.golang.org"}, project.GateNetwork)
	require.Equal(t, []string{"~/go/pkg", "out"}, project.GateAllowWrite)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.GateNetwork, "the default is no network")

	_, problems = loadProjectText(t, call(goodProject+`, gate_network = ["https://x.org/a"]`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "not a domain name")

	_, problems = loadProjectText(t, call(goodProject+`, gate_allow_write = [".git/hooks"]`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "allow_write lists")
}
