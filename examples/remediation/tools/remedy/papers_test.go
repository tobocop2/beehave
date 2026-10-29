package remedy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/model"
)

const (
	goodHandoff = "Regressions of this pull request so far: 0\nTokens at handoff: 1200\n"
	laneRow     = `{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":40000,"cache_creation_input_tokens":5000,"output_tokens":200}}}`
)

// textFile writes the text in a new folder and returns the path.
func textFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

// changes is a fake git that answers every call with the changed paths.
func changes(names string) remedy.Deps {
	return remedy.Deps{Git: git(func([]string) model.CommandResult { return model.CommandResult{Stdout: names} })}
}

func TestPapers_ASourcePathIsExitCodeOne(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, changes("reports/proof.md\ncrates/a/src/lib.rs\n"), "papers", "A..B", "--globs", "crates/*/src/*")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "REFUSED: A..B changes 1 source path(s)")
	assert.Contains(t, out, "crates/a/src/lib.rs")
}

func TestPapers_AMalformedPatternIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, changes("src/a.rs\n"), "papers", "A..B", "--globs", "[")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, `the pattern "[" is not valid`)
}

func TestPapers_NoGlobsTreatsDocumentsAsPapersAndPasses(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, changes("docs/a.md\ndocs/a.txt\n"), "papers", "A..B")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "OK: A..B changes no source path.\n", out)
	assert.Empty(t, errOut)
}

func TestPapers_ARangeThatGitCannotReadIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	deps := remedy.Deps{Git: git(func([]string) model.CommandResult { return model.CommandResult{ExitCode: 128} })}

	code, out, errOut := run(t, deps, "papers", "nosuchref..HEAD")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "git cannot read the range nosuchref..HEAD")
}

func TestPapers_ABadRangeAndABadCallShowTheUsage(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"papers"}, {"papers", "HEAD"}, {"papers", "A..B", "extra"}, {"papers", "A..B", "--nope"}} {
		code, out, errOut := run(t, changes(""), args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.NotEmpty(t, errOut, args)
	}
}

func TestHandoff_AWholeHandoffPasses(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "handoff", textFile(t, "prev.md", goodHandoff))

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "OK: regressions=0 tokens=1200\n", out)
	assert.Empty(t, errOut)
}

func TestHandoff_AHandoffWithNoRegressionLineIsExitCodeOneAndNamesTheFile(t *testing.T) {
	t.Parallel()

	path := textFile(t, "prev.md", "Tokens at handoff: 10\n")

	code, out, _ := run(t, remedy.Deps{}, "handoff", path, "--name", "the-handoff")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "REFUSED: the-handoff has no line 'Regressions of this pull request so far: <n>'")
}

func TestHandoff_ALaunchedJobThatNobodyReadIsExitCodeOneAndNamesTheCap(t *testing.T) {
	t.Parallel()

	path := textFile(t, "prev.md", "Regressions of this pull request so far: 1\nTokens at handoff: 200\nlaunched: logs/a.log\n")

	code, out, _ := run(t, remedy.Deps{}, "handoff", path, "--cap", "100")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "and the lane is at 200 tokens, over the cap of 100")
}

func TestHandoff_TheTranscriptGivesTheTokensWhenTheHandoffHoldsNone(t *testing.T) {
	t.Parallel()

	handoff := textFile(t, "prev.md", "Regressions of this pull request so far: 0\n")
	lane := textFile(t, "lane.jsonl", laneRow+"\n")

	code, out, _ := run(t, remedy.Deps{}, "handoff", handoff, "--transcript", lane)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "OK: regressions=0 tokens=")
	assert.NotContains(t, out, "tokens=\n")
}

func TestHandoff_NoTokensAndNoTranscriptIsExitCodeOne(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, remedy.Deps{}, "handoff", textFile(t, "prev.md", "Regressions of this pull request so far: 0\n"))

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "the spend of this lane is unknown")
}

func TestHandoff_ABadCapAnAbsentFileAndAnUnreadableTranscriptAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	good := textFile(t, "prev.md", goodHandoff)
	noTokens := textFile(t, "none.md", "Regressions of this pull request so far: 0\n")

	cases := map[string][]string{
		"bad cap":         {"handoff", good, "--cap", "0"},
		"absent file":     {"handoff", filepath.Join(t.TempDir(), "nope.md")},
		"bad transcript":  {"handoff", noTokens, "--transcript", filepath.Join(t.TempDir(), "nope.jsonl")},
		"no argument":     {"handoff"},
		"extra argument":  {"handoff", good, "extra"},
		"unknown flag":    {"handoff", good, "--nope"},
		"bad cap in text": {"handoff", good, "--cap", "many"},
	}

	for name, args := range cases {
		code, _, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.NotEmpty(t, errOut, name)
	}
}

func TestAuthors_OurCommitsUnderOurAddressPass(t *testing.T) {
	t.Parallel()

	path := textFile(t, "authors.txt", "abc me@x me\ndef them@y them\n")

	code, out, _ := run(t, remedy.Deps{}, "authors", path, "--email", "me@x", "--login", "me")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "author check: 2 commits, 1 ours and correctly addressed\n", out)
}

func TestAuthors_ACommitOfOursUnderAnotherAddressIsExitCodeOne(t *testing.T) {
	t.Parallel()

	path := textFile(t, "authors.txt", "abc other@x me\n")

	code, out, errOut := run(t, remedy.Deps{}, "authors", path, "--email", "me@x", "--login", "me")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "these commits are OURS but carry the wrong address")
}

func TestAuthors_NoIdentityAnEmptyFileAndAnAbsentFileAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	full := textFile(t, "authors.txt", "abc me@x me\n")
	empty := textFile(t, "empty.txt", "")

	cases := map[string][]string{
		"no identity":    {"authors", full},
		"empty file":     {"authors", empty, "--email", "me@x", "--login", "me"},
		"absent file":    {"authors", filepath.Join(t.TempDir(), "nope.txt"), "--email", "me@x", "--login", "me"},
		"no argument":    {"authors"},
		"extra argument": {"authors", full, "extra"},
		"unknown flag":   {"authors", full, "--nope"},
	}

	for name, args := range cases {
		code, _, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.NotEmpty(t, errOut, name)
	}
}
