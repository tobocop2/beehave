package agent

import (
	"slices"
	"sort"
	"strings"
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

// buildEnv returns the NAME=value list for an agent: the allowlisted names of base, the login folder and the token.
func buildEnv(base map[string]string, p Profile, dir, token string) []string {
	env := allowed(base)

	for _, name := range p.ConfigVars {
		env[name] = dir
	}

	if token != "" && p.TokenVar != "" {
		env[p.TokenVar] = token
	}

	list := make([]string, 0, len(env))
	for name, value := range env {
		list = append(list, name+"="+value)
	}

	sort.Strings(list)

	return list
}
