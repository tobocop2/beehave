package agent

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

// SandboxPlan is what an adapter needs to confine the shell of one run. Every path is absolute, except that an adapter may write the home folder as "~/".
// Work is the work folder, GitDirs the shared git directories that a linked work tree needs, and Read the extra paths the shell may read.
// Deny lists the credential paths: the adapter denies them for read and write, and for the file tools. Network lists the domains the shell may reach.
// DenyPatterns lists absolute glob patterns that the adapter denies in the same way, so that the CLI matches them when a file is used and
// a file that does not exist yet is covered too. DenyWrite lists absolute paths that stay readable but that the adapter denies for write, in the
// sandbox and in the file tools. AllowWrite lists the extra paths the shell may write.
type SandboxPlan struct {
	Work         string
	GitDirs      []string
	Read         []string
	Deny         []string
	DenyPatterns []string
	DenyWrite    []string
	Network      []string
	AllowWrite   []string
}

// Join returns the lists one after another, as a list that is never nil, so that the JSON of a sandbox setting holds [] and never null.
func Join(lists ...[]string) []string {
	if all := slices.Concat(lists...); all != nil {
		return all
	}

	return []string{}
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

// versionProblem runs the CLI with --version and says why the sandbox cannot start, or returns "" when the version is new enough.
// A CLI that ignores a sandbox setting it does not know runs with no sandbox and exits with code 0, so an old or unreadable version stops the run.
func versionProblem(ctx context.Context, spec Spec, p Profile, binary string, env []string, version versionOf) string {
	if spec.Sandbox == nil {
		return ""
	}

	out, err := version(ctx, binary, env)
	if err != nil {
		return fmt.Sprintf("cannot read the version of %s (%v), so the sandbox is not started. Set sandbox = None on the step to run it with no sandbox.", p.Binary, err)
	}

	return versionText(p, out)
}

// versionText says why the version that the CLI printed is not enough for the sandbox, or returns "" when it is enough.
func versionText(p Profile, out string) string {
	found := versionNumber.FindStringSubmatch(out)
	if found == nil {
		return fmt.Sprintf("the version of %s is not readable from %q, so the sandbox is not started. Update it to %s or later.", p.Binary, out, p.MinVersion)
	}

	least := versionNumber.FindStringSubmatch(p.MinVersion)
	if least == nil {
		return fmt.Sprintf("the profile of %s names the minimum version %q, which is not a version number, so the sandbox is not started.", p.Binary, p.MinVersion)
	}

	if semver.Compare(semverOf(found), semverOf(least)) < 0 {
		return fmt.Sprintf("%s is at version %s.%s.%s and the sandbox needs %s or later. Update it, or set sandbox = None on the step.", p.Binary, found[1], found[2], found[3], p.MinVersion)
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
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = env

	out, err := cmd.Output()

	return string(out), err
}

// hostOS is the operating system of this process.
func hostOS() string { return runtime.GOOS }
