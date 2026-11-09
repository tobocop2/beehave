package agent

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/tobocop2/beehave/internal/proc"
)

// SandboxPlan is what an adapter needs to confine the shell of one run. Every path is absolute, except that an adapter may write the home folder as "~/".
// Work is the work folder, GitDirs the shared git directories that a linked work tree needs, and Read the extra paths the shell may read.
// Deny lists the credential paths: the adapter denies them for read and write, and for the file tools. Network lists the domains the shell may reach.
// DenyPatterns lists absolute glob patterns that the adapter denies in the same way, so that the CLI matches them when a file is used and
// a file that does not exist yet is covered too. DenyWrite lists absolute paths that stay readable but that the adapter denies for write, in the
// sandbox and in the file tools. DenyWritePatterns lists absolute glob patterns that stay readable and that the adapter denies for write in the same
// way as DenyWrite, so that the CLI matches them when a file is used and a path that does not exist yet is covered too.
// AllowWrite lists the extra paths the shell may write.
type SandboxPlan struct {
	Work              string
	GitDirs           []string
	Read              []string
	Deny              []string
	DenyPatterns      []string
	DenyWrite         []string
	DenyWritePatterns []string
	Network           []string
	AllowWrite        []string
}

// versionOf reads the version that the CLI prints for --version.
type versionOf func(ctx context.Context, binary string, env []string) (string, error)

var versionNumber = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// sandboxProblem says why a run with a sandbox cannot start, or returns "" when it can. It does not start the CLI.
func sandboxProblem(spec Spec, p Profile, goos string) string {
	if spec.Sandbox == nil {
		return ""
	}

	switch {
	case !p.Sandbox:
		return p.Binary + " has no sandbox, so a step with the sandbox on does not start. Set sandbox = None on the step to run it with no sandbox."
	case goos == "windows":
		return p.Binary + " runs commands with no sandbox on Windows, so a step with the sandbox on does not start. Set sandbox = None on the step to run it with no sandbox."
	}

	return ""
}

// guardProblem says why a run with a guard cannot start on this system, or returns "" when it can.
func guardProblem(spec Spec, p Profile, goos string) string {
	if spec.Guard != "" && p.GuardPosixOnly && goos == "windows" {
		return p.Binary + " checks commands with a POSIX shell, so a step with forbid_commands does not start on Windows. Remove forbid_commands from the step, or run it on Linux or macOS."
	}

	return ""
}

// versionNeed is one reason to read the version of the CLI: the oldest version that the reason needs, what stops when the version is not enough, and how
// the step author turns the reason off.
type versionNeed struct{ least, subject, stop, off string }

// versionNeeds lists what the spec needs from the version of the CLI. A sandbox needs MinVersion. A guard needs GuardMinVersion, when the profile names one.
func versionNeeds(spec Spec, p Profile) []versionNeed {
	var needs []versionNeed

	if spec.Sandbox != nil {
		needs = append(needs, versionNeed{p.MinVersion, "the sandbox", "the sandbox is not started", "set sandbox = None on the step to run it with no sandbox"})
	}

	if spec.Guard != "" && p.GuardMinVersion != "" {
		needs = append(needs, versionNeed{p.GuardMinVersion, "the command guard", "the step is not started", "remove forbid_commands from the step to run it with no check of its commands"})
	}

	return needs
}

// versionProblem runs the CLI with --version and says why the run cannot start, or returns "" when the version is new enough for the sandbox and the guard.
// A CLI that ignores a sandbox setting or a hook it does not know runs with no sandbox or no guard and exits with code 0, so an old or unreadable version stops the run.
func versionProblem(ctx context.Context, spec Spec, p Profile, binary string, env []string, version versionOf) string {
	needs := versionNeeds(spec, p)
	if len(needs) == 0 {
		return ""
	}

	out, err := version(ctx, binary, env)
	if err != nil {
		return fmt.Sprintf("cannot read the version of %s (%v), so %s. To go on, %s.", p.Binary, err, needs[0].stop, needs[0].off)
	}

	for _, need := range needs {
		if problem := versionText(p, out, need); problem != "" {
			return problem
		}
	}

	return ""
}

// versionText says why the version that the CLI printed is not enough for the need, or returns "" when it is enough.
func versionText(p Profile, out string, need versionNeed) string {
	found := versionNumber.FindStringSubmatch(out)
	if found == nil {
		return fmt.Sprintf("the version of %s is not readable from %q, so %s. Update it to %s or later.", p.Binary, out, need.stop, need.least)
	}

	least := versionNumber.FindStringSubmatch(need.least)
	if least == nil {
		return fmt.Sprintf("the profile of %s names the minimum version %q, which is not a version number, so %s.", p.Binary, need.least, need.stop)
	}

	if semver.Compare(semverOf(found), semverOf(least)) < 0 {
		return fmt.Sprintf("%s is at version %s.%s.%s and %s needs %s or later, so %s. Update it, or %s.", p.Binary, found[1], found[2], found[3], need.subject, need.least, need.stop, need.off)
	}

	return ""
}

// semverOf writes the three numbers of a match as a semantic version. The leading zeros go, because semver refuses a number with one.
func semverOf(found []string) string {
	var numbers []string
	for _, digits := range found[1:] {
		numbers = append(numbers, cmp.Or(strings.TrimLeft(digits, "0"), "0"))
	}

	return "v" + strings.Join(numbers, ".")
}

// runVersion starts the CLI with --version in the environment of the run and returns what it prints.
func runVersion(ctx context.Context, binary string, env []string) (string, error) {
	result := proc.ExecRunner{Binary: binary, Only: env}.Run(ctx, []string{"--version"})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("%s --version exited with code %d: %s", binary, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result.Stdout, nil
}

// hostOS is the operating system of this process.
func hostOS() string { return runtime.GOOS }
