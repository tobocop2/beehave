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

// OwnerLogin is the credential settings that a git call to a remote takes as the owner, and nothing else of the git config of the owner.
type OwnerLogin struct {
	settings []string
}

// OwnerLoginOf returns the login with the -c arguments that Settings gave, as a record of an earlier read holds them.
func OwnerLoginOf(settings []string) OwnerLogin {
	return OwnerLogin{settings: slices.Clone(settings)}
}

// scopes returns the options that name the config files of the owner that carry credential settings.
func scopes() []string { return []string{"--system", "--global"} }

// ReadOwnerLogin reads the credential settings of the system and global git config of the owner, with the files that they include.
func ReadOwnerLogin(ctx context.Context) (OwnerLogin, error) {
	var found OwnerLogin

	for _, scope := range scopes() {
		out := start(New("", proc.DefaultTimeout).ExecRunner, true).
			Run(ctx, []string{configCommand, scope, "--includes", "--null", "--get-regexp", `^credential\.`})

		switch {
		case out.ExitCode == 0:
			found.settings = append(found.settings, parseSettings(out.Stdout)...)
		case out.ExitCode != 1:
			return OwnerLogin{}, fmt.Errorf("read the credential settings of the git config of the owner (%s): git exited with code %d: %s",
				scope, out.ExitCode, failure(out))
		}
	}

	return found, nil
}

// failure returns the reason of a failed call: the cause when git did not start, else what git printed.
func failure(out model.CommandResult) string {
	if out.SetupErr != nil {
		return out.SetupErr.Error()
	}

	return strings.TrimSpace(out.Stderr)
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

// ownerKept returns the variables of the process that an owner call keeps: the kept ones, the ssh agent socket and the names that a credential helper reads.
func ownerKept() []string {
	return append(kept(false), model.SSHAgentVar, "XDG_CONFIG_HOME", "GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR")
}

// Start returns spec as the runner of an owner call: the kept variables only, env, and then the variables that switch every git config file off.
func (o OwnerLogin) Start(spec proc.ExecRunner, env ...string) proc.ExecRunner {
	spec.Only = keep(ownerKept())
	spec.Env = append(append(slices.Clone(spec.Env), env...), userConfigOff()...)

	return spec
}
