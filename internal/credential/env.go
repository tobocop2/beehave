package credential

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// networkNames lists the only variables that a role runner takes from the engine. It holds no home folder, no login folder and no token.
func networkNames() []string {
	return []string{
		"PATH", "TMPDIR", "LANG",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
}

// RoleEnv returns the environment of a child that runs as a role: the network names of base and the pairs that the role gives, as NAME=value.
// The environment of the child is built from these, never from the whole environment of the engine, so a token that the role does not give cannot reach it.
func RoleEnv(base map[string]string, pairs ...string) []string {
	env := []string{}

	for _, name := range networkNames() {
		if value, found := base[name]; found {
			env = append(env, name+"="+value)
		}
	}

	slices.Sort(env)

	return append(env, pairs...)
}

// forbiddenNames lists the variables that an agent or a command step must never get.
func forbiddenNames() []string {
	return []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN", model.SSHAgentVar}
}

// Probe checks an environment, as NAME=value pairs, that the engine builds for an agent or a command step. It refuses an environment that holds
// a forbidden name or the value of one of the tokens.
func Probe(env []string, tokens ...string) error {
	for _, pair := range env {
		name, value, _ := strings.Cut(pair, "=")

		if slices.Contains(forbiddenNames(), name) {
			return fmt.Errorf("the environment of an agent step holds %s. Remove it from the environment of the engine", name)
		}

		for _, token := range tokens {
			if token != "" && strings.Contains(value, token) {
				return fmt.Errorf("the environment of an agent step holds a credential in %s. Remove it from the environment of the engine", name)
			}
		}
	}

	return nil
}
