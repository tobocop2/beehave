package agent

import (
	"maps"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const localePrefix = "LC_"

// inheritedNames lists the only variables an agent takes from the caller. A credential is never one of them.
func inheritedNames() []string {
	return []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"SSH_AUTH_SOCK",
	}
}

// sandboxDropped lists the allowlisted variables that a sandboxed run does not get. A proxy variable of the owner could send a tool past the
// proxy of the sandbox, and the agent socket reaches the keys of the owner.
func sandboxDropped() []string {
	return []string{
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSH_AUTH_SOCK",
	}
}

// sandboxEnv lists the variables that a sandboxed run sets, so that git reads no config file of the owner. The sandbox denies the home folder.
func sandboxEnv() map[string]string {
	return map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}
}

// allowed returns the variables of base that an agent may inherit.
func allowed(base map[string]string) map[string]string {
	names := inheritedNames()
	env := map[string]string{}

	for name, value := range base {
		if slices.Contains(names, name) || strings.HasPrefix(name, localePrefix) {
			env[name] = value
		}
	}

	return env
}

// LoginEnv returns the NAME=value list for a CLI that needs no token: the allowlisted names of base and the login folder.
func LoginEnv(base map[string]string, p Profile, dir string) []string {
	return buildEnv(base, p, dir, "")
}

// envFor returns the environment of one run: the allowlisted names, the login folder and the token.
// A sandboxed run gets no proxy variable and no agent socket, and sets the two git variables.
// The variables of the launch come after the allow-list. The git variables of a sandboxed run, the login folder and the token come after them, so a launch cannot change those.
func envFor(spec Spec, p Profile, launch map[string]string) []string {
	env := allowed(spec.Env)

	if spec.Sandbox != nil {
		for _, name := range sandboxDropped() {
			delete(env, name)
		}
	}

	maps.Copy(env, launch)

	if spec.Sandbox != nil {
		maps.Copy(env, sandboxEnv())
	}

	return list(env, p, spec.Runner.ConfigDir, spec.Token)
}

// buildEnv returns the NAME=value list for an agent: the allowlisted names of base, the login folder and the token.
func buildEnv(base map[string]string, p Profile, dir, token string) []string {
	return list(allowed(base), p, dir, token)
}

// list adds the login folder and the token to env and returns it as a sorted NAME=value list.
func list(env map[string]string, p Profile, dir, token string) []string {
	for _, name := range p.ConfigVars {
		env[name] = dir
	}

	if token != "" && p.TokenVar != "" {
		env[p.TokenVar] = token
	}

	return model.EnvPairs(env)
}
