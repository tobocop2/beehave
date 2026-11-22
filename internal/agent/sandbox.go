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
// ReadDirs lists the folders outside the work folder that the run may read, as working directories that the adapter passes to the CLI. An allow rule does not open a path to the file tools when
// the CLI blocks reads outside the working directories, so each folder here holds only the files that the run may read.
// Identity is the git identity of the engine at the work folder, which the commits of the shell carry.
// Edits is true for a run that may edit the files of the work folder with the file tools. The runner sets it from Spec.NoEdits.
// Temp is the short temporary folder of the run, which the runner makes before the launch and removes after it.
type SandboxPlan struct {
	Edits             bool
	Temp              string
	Work              string
	GitDirs           []string
	Read              []string
	ReadDirs          []string
	Deny              []string
	DenyPatterns      []string
	DenyWrite         []string
	DenyWritePatterns []string
	Network           []string
	Identity          GitIdentity
}

// GitIdentity is the user.name and the user.email of a git commit.
type GitIdentity struct {
	Name  string
	Email string
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

// versionNeeds lists what the spec needs from the version of the CLI. A sandbox needs MinVersion.
func versionNeeds(spec Spec, p Profile) []versionNeed {
	var needs []versionNeed

	if spec.Sandbox != nil {
		needs = append(needs, versionNeed{p.MinVersion, "the sandbox", "the sandbox is not started", "set sandbox = None on the step to run it with no sandbox"})
	}

	return needs
}

// rangeText names the versions of the CLI that the adapter supports, such as "1.19.5 up to but not including 2.0.0".
func rangeText(p Profile) string {
	return p.SupportedFrom + " up to but not including " + p.SupportedBefore
}

// versionProblem runs the CLI with --version and says why the run cannot start, or returns "" when the version is in the supported range of the profile and
// new enough for the sandbox. A CLI that ignores a sandbox setting or a hook it does not know runs with no sandbox or no guard and exits with code 0, and a
// CLI of a line that the adapter has no recording of can print a shape that the adapter reads wrong, so an old, new or unreadable version stops the run.
// It also returns what the CLI printed, which is "" when the version was not read.
func versionProblem(ctx context.Context, spec Spec, p Profile, binary string, env []string, version versionOf) (string, string) {
	needs := versionNeeds(spec, p)
	ranged := p.SupportedFrom != ""

	if len(needs) == 0 && !ranged {
		return "", ""
	}

	out, err := version(ctx, binary, env)
	if err != nil {
		return unreadable(p, err, needs, ranged), ""
	}

	problems := []string{rangeProblem(p, out), guardVersionProblem(spec, p, out)}

	for _, need := range needs {
		problems = append(problems, versionText(p, out, need))
	}

	return cmp.Or(problems...), out
}

// guardVersionProblem says why a step with a guard does not start on the version that the CLI printed, or returns "" when the step has no guard, the profile
// names no version from which the guard fails, or the version is below it.
func guardVersionProblem(spec Spec, p Profile, out string) string {
	found := versionNumber.FindStringSubmatch(out)
	if spec.Guard == "" || p.GuardBefore == "" || found == nil {
		return ""
	}

	if semver.Compare(semverOf(found), "v"+p.GuardBefore) < 0 {
		return ""
	}

	return fmt.Sprintf("%s is at version %s.%s.%s, and the guard of forbid_commands does not work from version %s on, so a step with forbid_commands is not started. Remove forbid_commands from the step, or install a version below %s.",
		p.Binary, found[1], found[2], found[3], p.GuardBefore, p.GuardBefore)
}

// versionArgs returns the arguments of the entries of Profile.VersionArgs that the version in out reaches. It returns none when out holds no version.
func versionArgs(p Profile, out string) []string {
	found := versionNumber.FindStringSubmatch(out)
	if found == nil {
		return nil
	}

	var args []string

	for _, entry := range p.VersionArgs {
		if semver.Compare(semverOf(found), "v"+entry.From) >= 0 {
			args = append(args, entry.Args...)
		}
	}

	return args
}

// unreadable says why a run does not start when the version of the CLI cannot be read.
func unreadable(p Profile, err error, needs []versionNeed, ranged bool) string {
	if ranged {
		return fmt.Sprintf("cannot read the version of %s (%v), so the step is not started. Install a version in the range that beehave supports: %s.", p.Binary, err, rangeText(p))
	}

	return fmt.Sprintf("cannot read the version of %s (%v), so %s. To go on, %s.", p.Binary, err, needs[0].stop, needs[0].off)
}

// rangeProblem says why the version that the CLI printed is outside the supported range of the profile, or returns "" when it is inside.
func rangeProblem(p Profile, out string) string {
	if p.SupportedFrom == "" {
		return ""
	}

	found := versionNumber.FindStringSubmatch(out)
	if found == nil {
		return fmt.Sprintf("the version of %s is not readable from %q, so the step is not started. Install a version in the range that beehave supports: %s.", p.Binary, out, rangeText(p))
	}

	got := semverOf(found)

	if semver.Compare(got, "v"+p.SupportedFrom) < 0 || semver.Compare(got, "v"+p.SupportedBefore) >= 0 {
		return fmt.Sprintf("%s is at version %s.%s.%s, which beehave does not support. It supports %s, so the step is not started. Install a version in that range.",
			p.Binary, found[1], found[2], found[3], rangeText(p))
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
