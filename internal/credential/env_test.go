package credential_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/credential"
)

func TestRoleEnv_HoldsTheNetworkNamesAndTheGivenPairsAndNothingElse(t *testing.T) {
	t.Parallel()

	base := map[string]string{"PATH": "/bin", "HOME": "/h", "GH_TOKEN": "owner", "SSH_AUTH_SOCK": "/s", "HTTPS_PROXY": "p", "GH_CONFIG_DIR": "/owner"}

	require.Equal(t, []string{"HTTPS_PROXY=p", "PATH=/bin", "GH_TOKEN=role"}, credential.RoleEnv(base, "GH_TOKEN=role"))
	require.NotNil(t, credential.RoleEnv(nil))
}

func TestProbe_RefusesAForbiddenNameOrATokenValueAndPassesAnOrdinaryEnvironment(t *testing.T) {
	t.Parallel()

	require.NoError(t, credential.Probe([]string{"PATH=/bin", "HOME=/h"}, "tok-secret", ""))

	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN", "SSH_AUTH_SOCK"} {
		require.ErrorContains(t, credential.Probe([]string{"PATH=/bin", name + "="}), name)
	}

	require.ErrorContains(t, credential.Probe([]string{"PATH=/bin", "NOTE=a tok-secret b"}, "tok-secret"), "in NOTE")
}
