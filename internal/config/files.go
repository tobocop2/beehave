package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	// runnersFileName is the file with the agent logins, the agent limit and the pool caps.
	runnersFileName = "runners.star"
	// oldRunnersFileName is the name that the runners file had before the config files became Starlark.
	oldRunnersFileName = "runners.hcl"
	// projectsDirName is the folder with one Starlark file for each project.
	projectsDirName = "projects"
	// projectExt is the file extension of a project file.
	projectExt = ".star"
	// oldProjectExt is the extension that a project file had before the config files became Starlark.
	oldProjectExt = ".hcl"
	// LocalHost is the build host that means this machine.
	LocalHost = "local"
	// GateSandboxRefusal says why a gate on this machine does not run when the sandbox is on.
	GateSandboxRefusal = "no sandbox enforcer can run on this machine, so the gate does not run here. " +
		"This holds for the project gate, for a gate call that names run, and for the gate of batch_merge. " +
		"Set sandbox = \"off\" in the project file, or build on a host. The project gate of a build host runs as before."
)

// clusterName is the form of a build host name: a letter, then letters, digits, dots and hyphens.
var clusterName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.-]*$`)

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// refuseOld refuses a file in the old format. beehave does not read it.
func refuseOld(path, name string) error {
	return refuse("%s: beehave no longer reads HCL config files. Rewrite the file as %s.", path, name)
}

// refuseAt returns a refusal that starts with the file, line and column of at.
// An %w cause stays reachable with errors.Is.
func refuseAt(at syntax.Position, format string, args ...any) error {
	return refuse("%s: "+format, append([]any{at}, args...)...)
}

func requireText(at syntax.Position, key, value string) error {
	if strings.TrimSpace(value) == "" {
		return refuseAt(at, "`%s` must be a non-empty string. Set it.", key)
	}

	return nil
}

// parseKey refuses an empty value of a key, then reads it with parse. A value that parse refuses is refused with the key and the reason.
//
//nolint:ireturn // T is a string type at every call: the linter reads an unconstrained T as an interface.
func parseKey[T any](at syntax.Position, key, value string, parse func(string) (T, error)) (T, error) {
	var zero T

	if err := requireText(at, key, value); err != nil {
		return zero, err
	}

	parsed, err := parse(value)
	if err != nil {
		return zero, refuseAt(at, "`%s`: %w. Fix the value.", key, err)
	}

	return parsed, nil
}

// expandHome replaces a leading "~" with the home folder.
func expandHome(path string) (string, error) {
	if !model.IsHomePath(path) {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", refuse("cannot expand `%s`: %w. Set HOME, or write the full path.", path, err)
	}

	return filepath.Join(home, path[1:]), nil
}
