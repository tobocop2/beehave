package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.starlark.net/syntax"
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
	GateSandboxRefusal = "no sandbox can enforce the gate on this machine yet, so the gate does not run here. " +
		"This holds for the project gate, for a gate call that names run, and for the gate of batch_merge. " +
		"Set sandbox = \"off\" in the project file, or build on a host. The project gate of a build host runs as before. The keys gate_network and gate_allow_write are kept for the day a sandbox can enforce them."
)

// The value goes into an ssh command, so a cluster name is a letter, then letters, digits, hyphens.
var clusterName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// refuseOld refuses a file in the old format. obee does not read it.
func refuseOld(path, name string) error {
	return refuse("%s: obee no longer reads HCL config files. Rewrite the file as %s.", path, name)
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

// expandHome replaces a leading "~" with the home folder.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", refuse("cannot expand `%s`: %w. Set HOME, or write the full path.", path, err)
	}

	return filepath.Join(home, path[1:]), nil
}
