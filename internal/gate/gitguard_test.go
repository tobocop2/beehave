package gate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// gitCall matches a git call that does not go through gpgit: git of the PATH, a git named by its path, or the resolved git by its variable.
var gitCall = regexp.MustCompile("(^|[\\s;&|(){}`\"'=]|\\$\\()(git|\\S*/git|\"\\$gpbin\"|\\$gpbin|\\$\\{gpbin\\})([\\s\"']|$)")

// gitBypasses returns each git call of the job text command that does not go through the constructor. The words that resolve the tools and that
// define gpgit and gpsrc are the constructor itself, so they are left out first.
func gitBypasses(command string, tools Tools) []string {
	for _, own := range []string{programFirst(tools), programGit(true)} {
		command = strings.ReplaceAll(command, own, "")
	}

	var found []string

	for _, word := range strings.Split(command, " && ") {
		if gitCall.MatchString(word) {
			found = append(found, word)
		}
	}

	return found
}

func TestLocalJob_RunsEveryGitCallThroughTheConstructor(t *testing.T) {
	t.Parallel()

	steps := programProjectOf()
	steps.Gate, steps.GateSteps = "", []model.GateSpec{{Name: "lint", Command: "make lint"}, {Name: "test", Command: "make test"}}
	noReport := programProjectOf()
	noReport.TestReport = ""

	for name, project := range map[string]model.Project{"gate_and_report": programProjectOf(), "gate_steps": steps, "no_report": noReport} {
		for toolsName, tools := range map[string]Tools{"resolved": localTools(t), "from_the_path": {}} {
			t.Run(name+"_"+toolsName, func(t *testing.T) {
				t.Parallel()

				src := jobSource{from: "/w/item", cloneURL: "https://forge.invalid/r.git", tools: tools, local: true}
				command := jobCommand(project, nil, src, "a1b2c3", "/w/root/job", "", "d4e5f6")

				require.Contains(t, command, "gpgit ", "the job has git calls")
				assert.Empty(t, gitBypasses(command, tools), "a git call of the local gate job does not go through gpgit")
			})
		}
	}
}

func TestRun_TheJobOfALocalGateRunsEveryGitCallThroughTheConstructor(t *testing.T) {
	t.Parallel()

	item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})
	g := localProgramGateEnv(t, item, localTools(t), nil)

	var jobs []string

	g.shell = func(_ context.Context, _, command string) (string, int) {
		jobs = append(jobs, command)

		return "", 1
	}

	_, err := g.Run(context.Background(), request)
	require.NoError(t, err)

	require.Len(t, jobs, 1, "the local gate runs one job")
	require.Contains(t, jobs[0], "gpgit ", "the job has git calls")
	assert.Empty(t, gitBypasses(jobs[0], localTools(t)), "a git call of the job that the local gate runs does not go through gpgit")
}

// failingGitPath makes a folder with a git that records its call in a marker file and exits 97, and returns the folder and the marker path.
func failingGitPath(t *testing.T) (string, string) {
	t.Helper()

	dir, marker := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + marker + "\nexit 97\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700)) //nolint:gosec // a test fake that must be executable

	return dir, marker
}

// plantedJob runs the job of a local gate with a failing git first on its PATH, after word is planted in the job text after the clone is entered.
// It returns the calls that the failing git recorded.
func plantedJob(t *testing.T, word string) string {
	t.Helper()

	item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})
	fake, marker := failingGitPath(t)
	env := []string{"PATH=" + fake + ":" + localCleanPath, "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	g := localProgramGateEnv(t, item, localTools(t), env)
	inner := g.shell
	planted := 0

	g.shell = func(ctx context.Context, where, command string) (string, int) {
		if word != "" {
			planted = strings.Count(command, " && cd tree")
			command = strings.Replace(command, " && cd tree", " && cd tree && { "+word+"; :; }", 1)
		}

		return inner(ctx, where, command)
	}

	_, err := g.Run(context.Background(), request)
	require.NoError(t, err)

	if word != "" {
		require.Equal(t, 1, planted, "the plant point occurs once in the job")
	}

	calls, err := os.ReadFile(marker) //nolint:gosec // the marker file of this test
	if os.IsNotExist(err) {
		return ""
	}

	require.NoError(t, err)

	return string(calls)
}

func TestGitGuard_AGitOnThePathThatFailsSeesNoCallOfTheJobItself(t *testing.T) {
	t.Parallel()

	assert.Empty(t, plantedJob(t, ""), "the job calls git only through its allow-listed start")
}

func TestGitGuard_AGitOnThePathThatFailsSeesAGitCallInAnySpelling(t *testing.T) {
	t.Parallel()

	for name, word := range map[string]string{
		"plain":               "git status",
		"backslash":           `\git status`,
		"command_substituted": `"$(command -v git)" status`,
		"split_quotes":        `gi''t status`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, "status\n", plantedJob(t, word), "a git call of the job outside the allow-listed start reaches the git of the PATH")
		})
	}
}

func TestGitGuard_FlagsEveryWayToCallGitPastTheConstructor(t *testing.T) {
	t.Parallel()

	words := []string{
		"git status",
		`[ -z "$(git ls-files)" ]`,
		`"$gpbin" checkout x`,
		"$gpbin fetch",
		"${gpbin} fetch",
		"/usr/bin/git rev-parse HEAD",
		"{ git rm -r x; }",
		"a | git hash-object --stdin",
		`sh -c "git log"`,
		`"git" status`,
		`command 'git' status`,
		"gpgit status",
		"gpsrc clone -- x tree",
		"cat .git/HEAD",
		`gpgit -C "$gpsnap/repo.git" fetch`,
		"gitleaks detect",
	}

	found := gitBypasses(strings.Join(words, " && "), Tools{})

	assert.Equal(t, words[:11], found)
}
