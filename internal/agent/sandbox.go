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
	result := proc.ExecRunner{Binary: binary, Only: env}.Run(ctx, []string{"--version"})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("%s --version exited with code %d: %s", binary, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result.Stdout, nil
}

// hostOS is the operating system of this process.
func hostOS() string { return runtime.GOOS }
