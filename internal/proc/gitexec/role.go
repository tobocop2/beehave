package gitexec

import "github.com/tobocop2/beehave/internal/credential"

// tokenVariable carries the token of one call to the credential helper of that call.
const tokenVariable = "BEEHAVE_FORGE_TOKEN"

// helper is a credential helper that answers with the token in tokenVariable. A leading ! makes git run it as a shell function.
const helper = `!f() { echo username=x-access-token; echo "password=$` + tokenVariable + `"; }; f`

// RoleEnv returns the environment of a git call that reaches a forge as the holder of token: the network names of base, a credential helper that answers
// with the token, and no config file of the owner (GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM). The first helper entry is empty, which clears every helper
// that an earlier level set. The token goes by the environment of the call, never by argv, which any process of the user can read.
func RoleEnv(token string, base map[string]string, pairs ...string) []string {
	own := []string{
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=" + helper,
		tokenVariable + "=" + token,
	}

	return credential.RoleEnv(base, append(own, pairs...)...)
}
