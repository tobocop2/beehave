package agent

import (
	"maps"
	"slices"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
)

const localePrefix = "LC_"

// traceparentVar is the W3C variable that names the parent span of a run.
const traceparentVar = "TRACEPARENT"

// inheritedNames lists the only variables an agent takes from the caller. A credential is never one of them.
func inheritedNames() []string {
	return []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	}
}

// sandboxDropped lists the allowlisted variables that a sandboxed run does not get. A proxy variable of the owner could send a tool past the proxy of the sandbox.
func sandboxDropped() []string {
	return []string{
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	}
}

// GitEnv returns the git variables of a sandboxed run: git reads no config file of the owner, which the sandbox denies, and commits carry the identity of the plan.
func (p SandboxPlan) GitEnv() map[string]string {
	return map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": p.Identity.Name, "GIT_AUTHOR_EMAIL": p.Identity.Email,
		"GIT_COMMITTER_NAME": p.Identity.Name, "GIT_COMMITTER_EMAIL": p.Identity.Email,
	}
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
// A sandboxed run gets no proxy variable, and sets the git variables of its plan: no config file of the owner, and the commit identity.
// The variables of the launch come after the allow-list, and the trace parent of the spec after those, so a launch cannot change it. The variables that turn editing off (NoEditEnv) come after those. The git variables of a sandboxed run, the login folder and the token come after them, so a launch cannot change those.
func envFor(spec Spec, p Profile, launch map[string]string) []string {
	env := allowed(spec.Env)

	if spec.Sandbox != nil {
		for _, name := range sandboxDropped() {
			delete(env, name)
		}
	}

	maps.Copy(env, launch)
	setTraceparent(env, spec.Traceparent)

	if spec.NoEdits {
		maps.Copy(env, p.NoEditEnv)
	}

	if spec.Sandbox != nil {
		maps.Copy(env, spec.Sandbox.GitEnv())
	}

	return list(env, p, spec.Runner.ConfigDir, spec.Token)
}

// setTraceparent sets the trace parent of the run in env. An empty value leaves env as it is.
func setTraceparent(env map[string]string, traceparent string) {
	if traceparent != "" {
		env[traceparentVar] = traceparent
	}
}

// buildEnv returns the NAME=value list for an agent: the allowlisted names of base, the login folder and the token.
func buildEnv(base map[string]string, p Profile, dir, token string) []string {
	return list(allowed(base), p, dir, token)
}

// list adds the login folder and the token to env and returns it as a sorted NAME=value list. A runner with no login folder leaves the variables unset,
// so the CLI reads its default folder: a measured run of Claude Code with CLAUDE_CONFIG_DIR set to the empty text loads no skill of ~/.claude/skills,
// and the same run with the variable unset does.
func list(env map[string]string, p Profile, dir, token string) []string {
	for _, name := range p.ConfigVars {
		if dir != "" {
			env[name] = dir
		}
	}

	if token != "" && p.TokenVar != "" {
		env[p.TokenVar] = token
	}

	return model.EnvPairs(env)
}
