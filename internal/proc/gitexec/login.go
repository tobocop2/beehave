package gitexec

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// configCommand is the git command that reads the config files.
const configCommand = "config"

// OwnerLogin is what a git call that reaches a remote as the owner takes from the setup of the owner: the credential settings of the system and global git
// config, read once. Every other setting of those files stays out of the call, so a program that an agent named there (core.sshCommand, a hooks folder, a
// url rewrite) never runs and never redirects the call. The zero value holds no setting.
type OwnerLogin struct {
	settings []string
}

// scopes returns the options that name the config files of the owner that carry credential settings.
func scopes() []string { return []string{"--system", "--global"} }

// ReadOwnerLogin reads the credential settings (credential.helper and every credential.<url>.* key) of the system and global git config of the owner. It runs
// git config, which prints values and starts no program. A file that git cannot read is an error.
func ReadOwnerLogin(ctx context.Context) (OwnerLogin, error) {
	var found OwnerLogin

	for _, scope := range scopes() {
		out := start(New("", proc.DefaultTimeout).ExecRunner, true).
			Run(ctx, []string{configCommand, scope, "--null", "--get-regexp", `^credential\.`})

		switch {
		case out.ExitCode == 0:
			found.settings = append(found.settings, parseSettings(out.Stdout)...)
		case out.SetupErr != nil || out.ExitCode != 1:
			return OwnerLogin{}, fmt.Errorf("read the credential settings of the git config of the owner (%s): git exited with code %d: %s %w",
				scope, out.ExitCode, strings.TrimSpace(out.Stderr), out.SetupErr)
		}
	}

	return found, nil
}

// parseSettings returns the -c arguments for the entries of git config --null output: each entry is a key, a newline, the value and a NUL.
func parseSettings(out string) []string {
	var args []string

	for entry := range strings.SplitSeq(out, "\x00") {
		if entry == "" {
			continue
		}

		key, value, hasValue := strings.Cut(entry, "\n")
		if !hasValue {
			args = append(args, "-c", key)

			continue
		}

		args = append(args, "-c", key+"="+value)
	}

	return args
}

// Settings returns the git arguments that carry the credential settings, to put before the command of a call.
func (o OwnerLogin) Settings() []string {
	return slices.Clone(o.settings)
}

// ownerKept is the variables of the process that an owner call keeps: the kept ones, the socket of the agent that holds the keys of an ssh remote, and the
// names that a credential helper of the owner reads to find its token and its own config (the helper of gh does). They are variables of the engine process,
// never of a file that an agent wrote. XDG_CONFIG_HOME stays, because that helper reads its config there; GIT_CONFIG_GLOBAL switches off the git files in it.
func ownerKept() []string {
	return append(kept(false), model.SSHAgentVar, "XDG_CONFIG_HOME", "GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR")
}

// Start returns spec as the runner of a call that reaches a remote as the owner. Like start, it keeps only variables of the process (ownerKept), and no
// config file of the owner or the system applies: git gets the credential settings of Settings instead. env is added before the variables that switch the
// config files off. It allows every transport, because the remote names its own.
func (o OwnerLogin) Start(spec proc.ExecRunner, env ...string) proc.ExecRunner {
	spec.Only = keep(ownerKept())
	spec.Env = append(append(slices.Clone(spec.Env), env...), userConfigOff()...)

	return spec
}
