package link

import (
	"context"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// The values of the ssh settings that a host declared credentials = "none" may get.
const (
	agentOff       = "no"
	localeVariable = "LANG"
	localePrefix   = "LC_"
)

// sshSettings is what `ssh -G` says ssh gives the host: the agent setting, the variable patterns that it sends, and the NAME=value pairs that it sets.
type sshSettings struct {
	forwardAgent    string
	sendEnv, setEnv []string
}

// forcedOptions are the ssh options that every call of a link to a host declared credentials = "none" adds: no agent and no forwarded port.
func forcedOptions() []string { return []string{"ForwardAgent=no", "ClearAllForwardings=yes"} }

// WithCredentials returns a copy of the link for a host with the credentials declaration given. The link itself runs nothing.
func (l *Link) WithCredentials(credentials model.HostCredentials) *Link {
	declared := *l
	declared.credentials = credentials

	return &declared
}

// forced returns the options that every call of the link adds after the options of the link.
func (l *Link) forced() []string {
	if l.credentials != model.HostCredentialsNone {
		return nil
	}

	return forcedOptions()
}

// CheckCredentials refuses a link to a host declared credentials = "none" when ssh, after its config, would forward the agent to the host, send it a
// variable other than LANG and LC_*, or set a variable with a secret name. It runs `ssh -G` with the options of the link, which connects to nothing.
// It runs nothing for a host without the declaration.
func (l *Link) CheckCredentials(ctx context.Context) error {
	if l.credentials != model.HostCredentialsNone {
		return nil
	}

	result := l.ssh.Run(ctx, append([]string{"-G"}, l.head(nil)...))
	if result.ExitCode != 0 {
		// The text of ssh is left out: a line of the config that ssh cannot read can hold a secret.
		return Refusal("", "cannot read the ssh config of the build host %s: `ssh -G %s` exited %d. Fix the ssh config, or remove credentials = \"none\"", l.target, l.target, result.ExitCode)
	}

	return decodeSettings(result.Stdout).check(l.target)
}

// decodeSettings reads the output of `ssh -G`: one setting a line, the lowercase key, a space and the value.
func decodeSettings(text string) sshSettings {
	var settings sshSettings

	for line := range strings.Lines(text) {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")

		switch key {
		case "forwardagent":
			settings.forwardAgent = value
		case "sendenv":
			settings.sendEnv = append(settings.sendEnv, value)
		case "setenv":
			settings.setEnv = append(settings.setEnv, value)
		}
	}

	return settings
}

// check refuses settings that give the host a credential of the owner. Only "no" keeps the agent: a socket path forwards it too.
func (s sshSettings) check(target string) error {
	if s.forwardAgent != agentOff {
		return Refusal("", "the build host %s gets the SSH agent (ssh -G says forwardagent %s). A host with credentials = \"none\" must not. Set ForwardAgent no for %s in ~/.ssh/config, or remove credentials = \"none\"", target, s.forwardAgent, target)
	}

	for _, pattern := range s.sendEnv {
		if pattern != localeVariable && !strings.HasPrefix(pattern, localePrefix) {
			return Refusal("", "the build host %s gets the variables %s of this computer (ssh -G says sendenv %s). A host with credentials = \"none\" gets only LANG and LC_*. Remove SendEnv %s for %s in ~/.ssh/config, or remove credentials = \"none\"", target, pattern, pattern, pattern, target)
		}
	}

	return checkSetEnv(target, s.setEnv)
}

// checkSetEnv refuses a variable that ssh sets on the host when its name marks a secret. The refusal names the variable and never shows its value.
func checkSetEnv(target string, pairs []string) error {
	for _, pair := range pairs {
		if name, _, _ := strings.Cut(pair, "="); model.SecretEnvName(name) {
			return Refusal("", "the build host %s gets the variable %s, whose name marks a secret (ssh -G says setenv %s=...). A host with credentials = \"none\" must not. Remove SetEnv %s for %s in ~/.ssh/config, or remove credentials = \"none\"", target, name, name, name, target)
		}
	}

	return nil
}
