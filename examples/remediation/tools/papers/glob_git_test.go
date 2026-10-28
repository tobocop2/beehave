package papers_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/papers"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// gitRepo runs git in a scratch repository that git settings of the machine cannot reach.
func gitRepo(t *testing.T) proc.ExecRunner {
	t.Helper()

	dir := t.TempDir()
	run := proc.NewExecRunner("git", dir)
	run.Env = []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "HOME=" + dir}

	return run
}

func mustGit(t *testing.T, run proc.ExecRunner, args ...string) model.CommandResult {
	t.Helper()

	result := run.Run(t.Context(), append([]string{"-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...))
	require.Zero(t, result.ExitCode, "git %v: %s", args, result.Stderr)

	return result
}

// changedPaths commits the path in a new repository and returns the paths that `git diff` lists for the pathspecs.
func changedPaths(t *testing.T, name string, specs []string) []string {
	t.Helper()

	run := gitRepo(t)
	mustGit(t, run, "init", "-q", "-b", "main")
	mustGit(t, run, "commit", "-q", "--allow-empty", "-m", "base")

	full := filepath.Join(run.Dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
	require.NoError(t, os.WriteFile(full, []byte("x\n"), 0o600))

	mustGit(t, run, "add", "--", name)
	mustGit(t, run, "commit", "-q", "-m", "head")

	args := append([]string{"diff", "--name-only", "-z", "HEAD~1..HEAD", "--"}, specs...)
	out := mustGit(t, run, args...).Stdout

	var names []string

	for _, one := range strings.Split(out, "\x00") {
		if one != "" {
			names = append(names, one)
		}
	}

	return names
}

// gitDiffers names the fixtures where a git pathspec gives another answer than the matcher of the papers check. The matcher keeps its own answer on each of them, so glob.go stays.
// A `^` negates a set in git, `?` matches one byte and not one rune, and a set with a reversed range is read another way.
var gitDiffers = map[string]bool{
	"caret_does_not_negate":            true,
	"unicode_one_rune":                 true,
	"negated_reversed_range_holds_all": true,
	"reversed_range_holds_nothing":     true,
}

func TestGlobs_GitPathspecAgreesWithTheMatcherOnEveryFixtureButTheNamedOnes(t *testing.T) {
	t.Parallel()

	for _, tc := range globCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			specs := strings.Fields(tc.glob)

			var viaGit bool
			if len(specs) > 0 {
				viaGit = len(changedPaths(t, tc.path, specs)) > 0
			}

			lines, err := papers.SourcePaths(t.Context(), diffRunner{model.CommandResult{Stdout: tc.path + "\n"}}, "A..B", tc.glob)
			require.NoError(t, err)

			viaOld := strings.HasPrefix(lines[0], "REFUSED")

			assert.Equal(t, tc.source, viaOld, "the matcher gives %v", viaOld)

			if gitDiffers[tc.name] {
				assert.NotEqual(t, viaOld, viaGit, "git now agrees on %s: the matcher can go", tc.name)

				return
			}

			assert.Equal(t, viaOld, viaGit, "matcher %v, git %v", viaOld, viaGit)
		})
	}
}
