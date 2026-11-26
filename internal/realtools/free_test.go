package realtools_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/proc"
)

// The rows of this file need no token and no network: they read the help of the pinned tools.

func TestRealGh_FieldListsEqualTheTable(t *testing.T) {
	requireSuite(t)

	run := ghOffline(t)

	version := run.Run(t.Context(), []string{"--version"})
	require.Zero(t, version.ExitCode, version.Stderr)
	require.Contains(t, version.Stdout, "gh version "+pinnedGh, "the gh on PATH is not the pinned gh: the table of the contract is the table of gh "+pinnedGh)

	for _, shape := range ghcontract.Shapes() {
		t.Run(strings.Join(shape.Words, "_"), func(t *testing.T) {
			got := helpOf(t, run, shape.Words)

			assert.ElementsMatch(t, shape.Fields, got.fields,"the --json fields of gh %s differ from gh-%s.txt: refresh the table with the pin", strings.Join(shape.Words, " "), pinnedGh)
		})
	}
}

func TestRealGh_FlagsOfEveryShapeExist(t *testing.T) {
	requireSuite(t)

	run := ghOffline(t)

	for _, shape := range ghcontract.Shapes() {
		t.Run(strings.Join(shape.Words, "_"), func(t *testing.T) {
			got := helpOf(t, run, shape.Words)

			for flag, takes := range shape.Flags {
				has, found := got.flags[flag]

				assert.True(t, found, "gh %s has no flag %s", strings.Join(shape.Words, " "), flag)
				assert.Equal(t, takes, has, "the flag %s of gh %s differs in taking a value", flag, strings.Join(shape.Words, " "))
			}
		})
	}
}

// launchFlags are the flags that beehave puts on the command line of Claude Code, read from the profile of the adapter and from the sandbox and usage launches.
// --max-turns is left out: the help of the CLI does not list it, and the print-mode row (TestRealClaude_PrintModeStream) runs it.
func launchFlags() []string {
	profile := claude.Adapter{}.Profile()

	flags := slices.Concat(profile.Args, profile.EditArgs, profile.Resume("id"), []string{profile.ModelFlag, profile.BudgetFlag})
	flags = append(flags, "--input-format", "--setting-sources", "--settings", "--add-dir", "--plugin-dir")

	flags = slices.DeleteFunc(flags, func(arg string) bool { return !strings.HasPrefix(arg, "-") })

	slices.Sort(flags)

	return slices.Compact(flags)
}

func TestRealClaude_FlagsOfEveryLaunchExist(t *testing.T) {
	requireSuite(t)

	run := proc.NewExecRunner("claude", "")

	done := run.Run(t.Context(), []string{"--help"})
	if done.ExitCode == 127 {
		t.Skip("claude is not on PATH: scripts/install-real-tools.sh installs the pinned claude with node (owed on the rig)")
	}

	require.Zero(t, done.ExitCode, done.Stderr)

	flags := launchFlags()
	require.GreaterOrEqual(t, len(flags), 10, "the launch flags were not read from the profile of the adapter")

	for _, flag := range flags {
		listed := regexp.MustCompile(`(?m)(^|[ ,])` + regexp.QuoteMeta(flag) + `([ ,=<\[]|$)`)

		assert.Regexp(t, listed, done.Stdout, "claude --help does not list the flag %s", flag)
	}
}
