package gitexec

import "github.com/tobocop2/beehave/internal/credential"

// tokenVariable carries the token of one call to the credential helper of that call. hostVariable carries the one host that the helper answers for.
const (
	tokenVariable = "BEEHAVE_FORGE_TOKEN"
	hostVariable  = "BEEHAVE_FORGE_HOST"
)

// helper is a credential helper of the call. A leading ! makes git run it as a shell function. It reads protocol= and host= from the input that git gives
// it, and it answers with the token only for the protocol https and the host in hostVariable. For any other host or protocol it prints nothing, so a url
// rewrite in a config file of the repository that sends the call to another host gets no token.
const helper = `!f() { h=; p=; while IFS== read -r k v; do case $k in host) h=$v;; protocol) p=$v;; esac; [ -z "$k" ] && break; done; ` +
	`[ "$p" = https ] && [ "$h" = "$` + hostVariable + `" ] || return 0; echo username=x-access-token; echo "password=$` + tokenVariable + `"; }; f`

// RoleEnv returns the environment of a git call that reaches the host of a forge as the holder of token: the network names of base, a credential helper that
// answers with the token for that host and for https only, and no config file of the owner (GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM). The first helper
// entry is empty, which clears every helper that an earlier level set. The token goes by the environment of the call, never by argv, which any process
// of the user can read. host is the host of the address that the call reaches, with its port when it has one.
func RoleEnv(token, host string, base map[string]string, pairs ...string) []string {
	own := []string{
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=" + helper,
		tokenVariable + "=" + token, hostVariable + "=" + host,
	}

	return credential.RoleEnv(base, append(own, pairs...)...)
}
