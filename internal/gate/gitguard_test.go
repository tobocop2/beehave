package gate

import (
	"context"
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
