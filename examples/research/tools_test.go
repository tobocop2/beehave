package research_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/exectest"
)

const (
	exitPass   = 0
	exitMiss   = 1
	exitNoRun  = 2
	exitOpen   = 3
	fileMode   = 0o600
	folderMode = 0o700
)

// server answers the path /ok with status 200, the path /redir with a redirect to /ok, and every other path with status 404.
func server(t *testing.T) string {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/redir":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/redir-dead":
			http.Redirect(w, r, "/missing", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)

	return s.URL
}

// notes writes one notes file per text into a new folder and returns the folder.
func notes(t *testing.T, texts ...string) string {
	t.Helper()

	folder := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(folder, folderMode))

	for i, text := range texts {
		name := filepath.Join(folder, string(rune('1'+i))+".md")
		require.NoError(t, os.WriteFile(name, []byte(text), fileMode))
	}

	return folder
}

// run runs a script of the example with the given environment and returns its output and exit code.
func run(t *testing.T, env []string, script string, args ...string) (string, int) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", append([]string{filepath.Join("tools", script)}, args...)...) //nolint:gosec // the test names its own script
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), exitPass
	}

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)

	return string(out), exit.ExitCode()
}

func TestCheck_AnswersForOneURLAndNotForTheOther(t *testing.T) {
	t.Parallel()

	base := server(t)
	good := notes(t, "# Q\n\n- A claim. [source: "+base+"/ok]\n")
	out, code := run(t, nil, "check.sh", good)
	assert.Equal(t, exitPass, code, out)
	assert.Empty(t, out)

	bad := notes(t, "# Q\n\n- A claim. [source: "+base+"/missing]\n")
	out, code = run(t, probe(base), "check.sh", bad)
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "FAIL "+bad+"/1.md: the source does not answer: "+base+"/missing")
}

// probe names the address that the check asks to tell a dead source from a dead network.
func probe(base string) []string {
	return []string{"CHECK_PROBE=" + base + "/ok"}
}

func TestCheck_FollowsARedirect(t *testing.T) {
	t.Parallel()

	base := server(t)
	out, code := run(t, probe(base), "check.sh", notes(t, "# Q\n\n- A claim. [source: "+base+"/redir]\n"))
	assert.Equal(t, exitPass, code, out)
	assert.Empty(t, out)

	// A redirect to a page that does not answer is a source that does not answer.
	out, code = run(t, probe(base), "check.sh", notes(t, "# Q\n\n- A claim. [source: "+base+"/redir-dead]\n"))
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "the source does not answer: "+base+"/redir-dead")
}

func TestCheck_ChecksEverySourceOfALine(t *testing.T) {
	t.Parallel()

	base := server(t)
	folder := notes(t,
		"# Q\n\n- Two sources, the first is dead. [source: "+base+"/gone] [source: "+base+"/ok]\n",
		"# Q\n\n- Two sources, the second is dead. [source: "+base+"/ok] [source: "+base+"/lost]\n",
		"# Q\n\n- Two sources, one is not a web address. [source: "+base+"/ok] [source: notes.txt]\n",
	)

	out, code := run(t, probe(base), "check.sh", folder)
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, folder+"/1.md: the source does not answer: "+base+"/gone")
	assert.Contains(t, out, folder+"/2.md: the source does not answer: "+base+"/lost")
	assert.Contains(t, out, "FAIL "+folder+"/3.md: the claim has no source")
	assert.NotContains(t, out, "does not answer: "+base+"/ok")
}

func TestCheck_EveryLineOfTheNotesIsAClaim(t *testing.T) {
	t.Parallel()

	base := server(t)
	good := "- A good claim. [source: " + base + "/ok]\n"
	folder := notes(t,
		"# Q\n\n"+good+"* A star claim with no source.\n",
		"# Q\n\n"+good+"1. A numbered claim with no source.\n",
		"# Q\n\n"+good+"A prose claim with no source.\n",
		"# Q\n\n"+good+"  \n\n",
		"# Q\n\n* A star claim. [source: "+base+"/ok]\n1. A numbered claim. [source: "+base+"/ok]\nA prose claim. [source: "+base+"/ok]\n",
	)

	out, code := run(t, probe(base), "check.sh", folder)
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "FAIL "+folder+"/1.md: the claim has no source: * A star claim with no source.")
	assert.Contains(t, out, "FAIL "+folder+"/2.md: the claim has no source: 1. A numbered claim with no source.")
	assert.Contains(t, out, "FAIL "+folder+"/3.md: the claim has no source: A prose claim with no source.")
	assert.NotContains(t, out, "/4.md")
	assert.NotContains(t, out, "/5.md")
}

func TestCheck_CannotRunWithoutANetwork(t *testing.T) {
	t.Parallel()

	base := server(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	dead := closed.URL
	closed.Close()

	// No source answers, and the known address does not answer either: the network is down, not the sources.
	out, code := run(t, []string{"CHECK_PROBE=" + dead + "/ok"}, "check.sh", notes(t, "# Q\n\n- A claim. [source: "+base+"/missing]\n"))
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "check: no network")
	assert.NotContains(t, out, "FAIL")

	// A source answers: the network is up, so the miss stays a miss and the probe is not needed.
	out, code = run(t, []string{"CHECK_PROBE=" + dead + "/ok"}, "check.sh", notes(t, "# Q\n\n- One. [source: "+base+"/ok]\n- Two. [source: "+base+"/missing]\n"))
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "the source does not answer: "+base+"/missing")
}

func TestCheck_StopsAtTheCapOfURLs(t *testing.T) {
	t.Parallel()

	base := server(t)
	folder := notes(t, "# Q\n\n- One. [source: "+base+"/ok]\n- Two. [source: "+base+"/ok]\n- Three. [source: "+base+"/missing]\n")

	out, code := run(t, []string{"CHECK_MAX_URLS=2"}, "check.sh", folder)
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "FAIL "+folder+"/1.md: the notes hold more than 2 sources. The check reads at most 2 URLs in one run")
	assert.NotContains(t, out, "does not answer")
}

func TestCheck_StopsAtTheTotalTime(t *testing.T) {
	t.Parallel()

	base := server(t)
	out, code := run(t, []string{"CHECK_TOTAL_TIME=0"}, "check.sh", notes(t, "# Q\n\n- One. [source: "+base+"/ok]\n"))
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "check: the check ran for more than 0 seconds")
}

func TestCheck_BoundsEachFetch(t *testing.T) {
	t.Parallel()

	// A curl that records its arguments stands in for the real one.
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "curl.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexit 0\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "curl"), script))

	out, code := run(t, []string{"PATH=" + bin + ":/usr/bin:/bin"}, "check.sh", notes(t, "# Q\n\n- One. [source: http://example.test/a]\n"))
	require.Equal(t, exitPass, code, out)

	text, err := os.ReadFile(log) //nolint:gosec // the test names its own log
	require.NoError(t, err)
	assert.Contains(t, string(text), "--connect-timeout 5")
	assert.Contains(t, string(text), "--max-time 10")
	assert.Contains(t, string(text), "http://example.test/a")
}

func TestCheck_NamesEachClaimWithNoSource(t *testing.T) {
	t.Parallel()

	base := server(t)
	folder := notes(t,
		"# Q\n\n- A claim with no source.\n",
		"# Q\n\n- A claim with a source that is not a web address. [source: notes.txt]\n",
		"# Q\n\n- A good claim. [source: "+base+"/ok]\n- A source that is not at the end. [source: "+base+"/ok] and more\n",
	)

	out, code := run(t, nil, "check.sh", folder)
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "FAIL "+folder+"/1.md: the claim has no source: - A claim with no source.")
	assert.Contains(t, out, "FAIL "+folder+"/2.md: the claim has no source")
	assert.Contains(t, out, "FAIL "+folder+"/3.md: the claim has no source: - A source that is not at the end.")
	assert.NotContains(t, out, "3.md: the source does not answer")
}

func TestCheck_RejectsNotesWithNoClaim(t *testing.T) {
	t.Parallel()

	out, code := run(t, nil, "check.sh", notes(t, "# Q\n\n  \n"))
	assert.Equal(t, exitMiss, code)
	assert.Contains(t, out, "the notes hold no claim")
}

func TestCheck_CannotRun(t *testing.T) {
	t.Parallel()

	out, code := run(t, nil, "check.sh", filepath.Join(t.TempDir(), "absent"))
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "is not a folder")

	out, code = run(t, nil, "check.sh")
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "is not a folder")

	out, code = run(t, nil, "check.sh", notes(t))
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "holds no notes")
}

func TestCheck_NeedsCurl(t *testing.T) {
	t.Parallel()

	// The path is an empty folder, so the check cannot find curl.
	cmd := exec.CommandContext(t.Context(), "/bin/sh", filepath.Join("tools", "check.sh"), notes(t, "# Q\n\n- A claim. [source: http://127.0.0.1/ok]\n")) //nolint:gosec // the test names its own script
	cmd.Env = []string{"PATH=" + t.TempDir()}

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, exitNoRun, exit.ExitCode())
	assert.Contains(t, string(out), "curl is not on the path")
}

func TestReport_HoldsTheNotesAndTheSources(t *testing.T) {
	t.Parallel()

	folder := notes(t,
		"# What is A?\n\n- A is a. [source: http://example.test/a]\n",
		"# What is B?\n\n- B is b. [source: http://example.test/b] [source: http://example.test/c]\n* B is also c. [source: http://example.test/a]\n",
	)
	report := filepath.Join(t.TempDir(), "out", "report.md")

	out, code := run(t, []string{"TITLE=Compare A and B", "SUMMARY=A is a and B is b."}, "report.sh", folder, report)
	require.Equal(t, exitPass, code, out)
	assert.Contains(t, out, "report: wrote "+report)

	text, err := os.ReadFile(report) //nolint:gosec // the test names its own report
	require.NoError(t, err)

	want := "# Compare A and B\n\n## Summary\n\nA is a and B is b.\n\n# What is A?\n\n- A is a. [source: http://example.test/a]\n\n# What is B?\n\n" +
		"- B is b. [source: http://example.test/b] [source: http://example.test/c]\n* B is also c. [source: http://example.test/a]\n\n" +
		"## Sources\n\n- http://example.test/a\n- http://example.test/b\n- http://example.test/c\n"
	assert.Equal(t, want, string(text))
}

func TestReport_LeavesOutAnEmptySummary(t *testing.T) {
	t.Parallel()

	report := filepath.Join(t.TempDir(), "report.md")
	_, code := run(t, nil, "report.sh", notes(t, "# What is A?\n\n- A is a. [source: http://example.test/a]\n"), report)
	require.Equal(t, exitPass, code)

	text, err := os.ReadFile(report) //nolint:gosec // the test names its own report
	require.NoError(t, err)
	assert.NotContains(t, string(text), "## Summary")
}

func TestReport_StartsWithTheOpenFindings(t *testing.T) {
	t.Parallel()

	folder := notes(t, "# What is A?\n\n- A is a. [source: http://example.test/a]\n")
	report := filepath.Join(t.TempDir(), "report.md")

	out, code := run(t, []string{"OPEN=notes/1.md: the source does not state the claim", "SUMMARY=A is a."}, "report.sh", folder, report)
	require.Equal(t, exitOpen, code, out)
	assert.Contains(t, out, "report: wrote "+report+". The review left findings open, so the item ends as stopped.")

	text, err := os.ReadFile(report) //nolint:gosec // the test names its own report
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(text), "# Research report\n\n## Open findings\n\n"), string(text))
	assert.Contains(t, string(text), "\nnotes/1.md: the source does not state the claim\n")
	assert.Contains(t, string(text), "\n## Summary\n\nA is a.\n")
}

func TestCheck_RefusesAValueThatIsNotAnInteger(t *testing.T) {
	t.Parallel()

	base := server(t)
	folder := notes(t, "# Q\n\n- One. [source: "+base+"/ok]\n")

	for _, env := range []string{"CHECK_MAX_URLS=abc", "CHECK_TOTAL_TIME=1.5", "CHECK_MAX_URLS=", "CHECK_TOTAL_TIME=-3"} {
		name, _, _ := strings.Cut(env, "=")
		out, code := run(t, []string{env}, "check.sh", folder)
		assert.Equal(t, exitNoRun, code, env)
		assert.Contains(t, out, "check: "+name+" must be a whole number", env)
	}
}

func TestCheck_PrintsTheFailuresFoundWhenTheTimeRunsOut(t *testing.T) {
	t.Parallel()

	// The check reads the time with date, in whole seconds. A date that reads 0 until curl has run and 1000 after puts the limit of one second
	// between the first fetch and the second on any machine. No real second passes, so a busy machine cannot pass the limit before the first fetch.
	bin := t.TempDir()
	ran := filepath.Join(bin, "curl-ran")
	curl := "#!/bin/sh\ntouch " + ran + "\nexit 22\n"
	date := "#!/bin/sh\nif [ -e " + ran + " ]; then echo 1000; else echo 0; fi\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "curl"), curl))
	require.NoError(t, exectest.Write(filepath.Join(bin, "date"), date))

	folder := notes(t, "# Q\n\n- One. [source: http://example.test/a]\n- Two. [source: http://example.test/b]\n")
	out, code := run(t, []string{"PATH=" + bin + ":/usr/bin:/bin", "CHECK_TOTAL_TIME=1"}, "check.sh", folder)

	// One source was checked and failed, so research can try again: the code is 1 and the failure comes before the message.
	assert.Equal(t, exitMiss, code, out)
	assert.Regexp(t, `(?s)FAIL .*/1\.md: the source does not answer: http://example.test/a.*check: the check ran for more than 1 seconds`, out)
	assert.NotContains(t, out, "example.test/b")
}

func TestCheck_StopsAtOnceForANotesFileWithNoSubQuestion(t *testing.T) {
	t.Parallel()

	base := server(t)
	good := "# Q\n\n- One. [source: " + base + "/ok]\n"
	folder := notes(t, good, good)
	require.NoError(t, os.WriteFile(filepath.Join(folder, "6.md"), []byte(good), fileMode))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "extra.md"), []byte(good), fileMode))

	// With two sub-questions, the file 6.md has none.
	out, code := run(t, []string{"CHECK_QUESTIONS=2"}, "check.sh", folder)
	assert.Equal(t, exitNoRun, code, out)
	assert.Contains(t, out, "FAIL "+folder+"/6.md: the notes file has no sub-question")
	assert.Contains(t, out, "check: a notes file has no sub-question: "+folder+"/6.md")

	// Without the number of sub-questions, only a name that is not a number is refused.
	require.NoError(t, os.Remove(filepath.Join(folder, "6.md")))
	out, code = run(t, nil, "check.sh", folder)
	assert.Equal(t, exitNoRun, code, out)
	assert.Contains(t, out, "check: a notes file has no sub-question: "+folder+"/extra.md")

	require.NoError(t, os.Remove(filepath.Join(folder, "extra.md")))
	out, code = run(t, []string{"CHECK_QUESTIONS=2"}, "check.sh", folder)
	assert.Equal(t, exitPass, code, out)
}

func TestReport_NeedsAFolderAndAFile(t *testing.T) {
	t.Parallel()

	out, code := run(t, nil, "report.sh", filepath.Join(t.TempDir(), "absent"), "x.md")
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "usage: report.sh")

	out, code = run(t, nil, "report.sh", notes(t, "# Q\n"))
	assert.Equal(t, exitNoRun, code)
	assert.Contains(t, out, "usage: report.sh")
}
