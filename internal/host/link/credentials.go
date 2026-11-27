package link

import (
	"context"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
)

// The values of the ssh settings that a host declared credentials = "none" may get.
const (
	forwardOff     = "no"
	delegationOn   = "yes"
	localeVariable = "LANG"
	localePrefix   = "LC_"
)

// sshSettings is what `ssh -G` says ssh gives the host: the agent, X11 and Kerberos delegation settings, the variable patterns that it sends, the
// NAME=value pairs that it sets, and the keys of the proxy settings that route the connection through another host.
type sshSettings struct {
	forwardAgent, forwardX11, delegation string
	sendEnv, setEnv, proxies             []string
}

// forcedOptions are the ssh options that every call of a link to a host declared credentials = "none" adds: no agent, no forwarded port, no X11
// display, no connection shared with an earlier ssh, which can hold an agent that the forced options of a later call cannot remove, and no
// Kerberos credential delegated to the host.
func forcedOptions() []string {
	return []string{
		"ForwardAgent=no", "ClearAllForwardings=yes", "ForwardX11=no", "ForwardX11Trusted=no", "ControlMaster=no", "ControlPath=none",
		"GSSAPIDelegateCredentials=no",
	}
}

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

// CheckCredentials refuses a link to a host declared credentials = "none" when ssh, after its config, would forward the agent, the X11 display or
// the Kerberos credentials to the host, send it a variable other than LANG and LC_*, set a variable with a secret name, or reach it through
// another host.
// It runs `ssh -G` with the options of the link, which connects to nothing.
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
		settings.read(key, value)
	}

	return settings
}

// read keeps one setting of `ssh -G` and drops a key the check does not read. A proxy setting is kept by its key only: a proxy command can hold
// a secret.
func (s *sshSettings) read(key, value string) {
	switch key {
	case "forwardagent":
		s.forwardAgent = value
	case "forwardx11":
		s.forwardX11 = value
	case "gssapidelegatecredentials":
		s.delegation = value
	case "proxyjump", "proxycommand":
		s.proxies = append(s.proxies, key)
	case "sendenv":
		s.sendEnv = append(s.sendEnv, value)
	case "setenv":
		s.setEnv = append(s.setEnv, value)
	}
}

// check refuses settings that give the host, or a host on the way to it, a credential of the owner.
func (s sshSettings) check(target string) error {
	if err := s.checkForwards(target); err != nil {
		return err
	}

	if err := checkProxies(target, s.proxies); err != nil {
		return err
	}

	if err := checkSendEnv(target, s.sendEnv); err != nil {
		return err
	}

	return checkSetEnv(target, s.setEnv)
}

// checkForwards refuses the agent, the X11 display and Kerberos delegation. For the agent and the display only "no" passes: a socket path
// forwards the agent too, and a missing line is not a "no".
func (s sshSettings) checkForwards(target string) error {
	if s.forwardAgent != forwardOff {
		return Refusal("", "the build host %s gets the SSH agent (ssh -G says forwardagent %s). A host with credentials = \"none\" must not. Set ForwardAgent no for %s in ~/.ssh/config, or remove credentials = \"none\"", target, s.forwardAgent, target)
	}

	if s.forwardX11 != forwardOff {
		return Refusal("", "the build host %s gets the X display of this computer (ssh -G says forwardx11 %s). A host with credentials = \"none\" must not. Set ForwardX11 no for %s in ~/.ssh/config, or remove credentials = \"none\"", target, s.forwardX11, target)
	}

	// An ssh built without GSSAPI prints no delegation line and delegates nothing, so only "yes" is refused.
	if s.delegation == delegationOn {
		return Refusal("", "the build host %s gets the Kerberos credentials of this computer (ssh -G says gssapidelegatecredentials yes). A host with credentials = \"none\" must not. Set GSSAPIDelegateCredentials no for %s in ~/.ssh/config, or remove credentials = \"none\"", target, target)
	}

	return nil
}

// checkProxies refuses a host that ssh reaches through another host. beehest cannot check what ssh gives that other host, such as the agent.
// The refusal names the key only: a proxy command can hold a secret.
func checkProxies(target string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	return Refusal("", "the build host %s is reached through another host (ssh -G sets %s). beehest cannot check what ssh gives that host, such as the SSH agent, so a host with credentials = \"none\" must not use ProxyJump or ProxyCommand. Remove it for %s in ~/.ssh/config, or remove credentials = \"none\" for this host", target, keys[0], target)
}

// checkSendEnv refuses a variable pattern that ssh sends to the host, other than LANG and LC_*.
func checkSendEnv(target string, patterns []string) error {
	for _, pattern := range patterns {
		if pattern != localeVariable && !strings.HasPrefix(pattern, localePrefix) {
			return Refusal("", "the build host %s gets the variables %s of this computer (ssh -G says sendenv %s). A host with credentials = \"none\" gets only LANG and LC_*. Remove SendEnv %s for %s in ~/.ssh/config, or remove credentials = \"none\"", target, pattern, pattern, pattern, target)
		}
	}

	return nil
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
