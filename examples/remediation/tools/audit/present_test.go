package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// based tags the base commit, so the tests can name the range base..main.
func based(t *testing.T) *repo {
	t.Helper()

	r := newRepo(t)
	r.git("tag", "base")

	return r
}

func presentLines(t *testing.T, r *repo) []string {
	t.Helper()

	lines, err := New(r).Present(context.Background(), "main", "base")
	require.NoError(t, err)

	return texts(lines)
}

func TestPresent_NoMergeFromTheBasePrintsTheHeaderAndTheDoneLine(t *testing.T) {
	t.Parallel()

	r := based(t)

	assert.Equal(t, []string{
		"AUDIT_TIP=" + r.git("rev-parse", "main"),
		"MERGES_ENUMERATED=0 (from the branch, not a fixed list)",
		"AUD19_PRESENT_DONE",
	}, presentLines(t, r))
}

func TestPresent_PullRequestMergeIsLabelledByItsNumber(t *testing.T) {
	t.Parallel()

	r := based(t)
	merged := r.pullMerge("feat", map[string]string{"f.txt": "one\ntwo\n"}, "")

	got := presentLines(t, r)

	assert.Equal(t, "MERGES_ENUMERATED=1 (from the branch, not a fixed list)", got[1])
	assert.Equal(t, "#12 "+merged+" files=1 touched_since=0 lines_gone_in_surviving_files=0 files_deleted=0", got[2])
}

func TestPresent_OtherMergeIsLabelledByItsHash(t *testing.T) {
	t.Parallel()

	r := based(t)
	merged := r.pullMerge("feat", map[string]string{"f.txt": "one\n"}, "back-merge")

	assert.True(t, strings.HasPrefix(presentLines(t, r)[2], "#m"+r.short(merged)+" "+merged+" files=1"))
}

func TestPresent_LabelTakesTheNumberUpToTheFirstSpace(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\n"}, "Merge pull request #7 from a b")

	assert.True(t, strings.HasPrefix(presentLines(t, r)[2], "#7 "))
}

func TestPresent_LinesTheTipNoLongerHasAreASignal(t *testing.T) {
	t.Parallel()

	r := based(t)
	merged := r.pullMerge("feat", map[string]string{"f.txt": "one\ntwo\nthree\n"}, "")
	r.commit("trim", map[string]string{"f.txt": "one\n"})

	assert.Equal(t,
		"#12 "+merged+" files=1 touched_since=1 lines_gone_in_surviving_files=2 files_deleted=0\n"+
			"    SIGNAL: 2 of 3 added lines gone, file still present: f.txt",
		presentLines(t, r)[2])
}

func TestPresent_FileDeletedAtTheTipIsReportedApart(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\ntwo\n"}, "")
	r.git("rm", "-q", "f.txt")
	r.git("commit", "-q", "-m", "retire")

	got := presentLines(t, r)[2]

	assert.True(t, strings.HasSuffix(got, "files_deleted=1\n    file deleted at tip (check it was a deliberate retirement): f.txt (2 lines)"))
	assert.Contains(t, got, "lines_gone_in_surviving_files=0")
}

func TestPresent_FileChangedSinceWithEveryLineKeptIsNotASignal(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\n"}, "")
	r.commit("more", map[string]string{"f.txt": "one\nmore\n"})

	got := presentLines(t, r)[2]

	assert.Contains(t, got, "touched_since=1 lines_gone_in_surviving_files=0 files_deleted=0")
	assert.NotContains(t, got, "SIGNAL")
}

func TestPresent_PathWithNonASCIICharactersIsReadByItsExactName(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"é.txt": "one\ntwo\n"}, "")
	r.commit("trim", map[string]string{"é.txt": "one\n"})

	assert.Contains(t, presentLines(t, r)[2], "SIGNAL: 1 of 2 added lines gone, file still present: é.txt")
}

func TestPresent_PathWithASpaceIsReadWhole(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"a b.txt": "one\ntwo\n"}, "")
	r.commit("trim", map[string]string{"a b.txt": "one\n"})

	assert.Contains(t, presentLines(t, r)[2], "present: a b.txt")
}

func TestPresent_AddedLinesThatStartWithPlusOrAreBlankAreNotCounted(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "++x\n\nkeep\n+y\n"}, "")
	r.commit("trim", map[string]string{"f.txt": "keep\n"})

	// "++x" is the diff line "+++x", a blank line is "+" alone and "+y" is "++y": none is counted. Only "keep" is, and it is still there.
	assert.Contains(t, presentLines(t, r)[2], "lines_gone_in_surviving_files=0")
}

func TestPresent_CarriageReturnIsPartOfTheLine(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\r\ntwo\r\n"}, "")
	r.commit("same", map[string]string{"f.txt": "one\r\ntwo\r\nmore\r\n"})

	assert.Contains(t, presentLines(t, r)[2], "lines_gone_in_surviving_files=0")

	r.commit("lf only", map[string]string{"f.txt": "one\ntwo\n"})

	assert.Contains(t, presentLines(t, r)[2], "SIGNAL: 2 of 2 added lines gone")
}

func TestPresent_LoneCarriageReturnDoesNotSplitALine(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\rtwo\nkeep\n"}, "")
	r.commit("split", map[string]string{"f.txt": "one\ntwo\nkeep\n"})

	assert.Contains(t, presentLines(t, r)[2], "SIGNAL: 1 of 2 added lines gone")
}

func TestPresent_PathNeverTouchedSinceIsSkipped(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\n"}, "")

	assert.Contains(t, presentLines(t, r)[2], "touched_since=0")
}

func TestPresent_MergeThatLeftTheBranchIsAFinding(t *testing.T) {
	t.Parallel()

	r := based(t)
	merged := r.pullMerge("feat", map[string]string{"f.txt": "one\n"}, "")
	parent := r.git("rev-parse", merged+"^1")

	reading := &reader{ctx: context.Background(), git: r}
	got := reading.presentMerge(parent, "12", merged)

	require.NoError(t, reading.err)
	assert.Equal(t, "#12 MERGE_NOT_ANCESTOR_OF_TIP "+merged+"  <-- FINDING: the merge left the branch", got)
}

func TestPresent_TipGitCannotReadIsARefusal(t *testing.T) {
	t.Parallel()

	r := based(t)

	_, err := New(r).Present(context.Background(), "nope", "base")

	require.ErrorContains(t, err, "git rev-parse nope failed")
}

func TestPresent_BaseGitCannotReadIsARefusal(t *testing.T) {
	t.Parallel()

	r := based(t)

	_, err := New(r).Present(context.Background(), "main", "nope")

	require.ErrorContains(t, err, "rev-list --merges nope..main failed")
}

func TestPresent_FileWithLinesTheMergeRemovedCountsOnlyAddedLines(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.branch("feat")
	r.commit("edit", map[string]string{"shared.txt": "base\nnew\n"})
	r.checkout("main")
	r.commit("m", map[string]string{"main.txt": "m\n"})
	r.merge("feat", "Merge pull request #3 from o/feat", nil)
	r.commit("revert", map[string]string{"shared.txt": "base\n"})

	assert.Contains(t, presentLines(t, r)[2], "SIGNAL: 1 of 1 added lines gone")
}

func TestPresent_MergeThatAddedNoLinesToATouchedFileLosesNone(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"empty.txt": ""}, "")
	r.commit("fill", map[string]string{"empty.txt": "later\n"})

	assert.Contains(t, presentLines(t, r)[2], "touched_since=1 lines_gone_in_surviving_files=0 files_deleted=0")
}

func TestPresent_EveryReadThatMustSucceedRefusesWhenItFails(t *testing.T) {
	t.Parallel()

	r := based(t)
	r.pullMerge("feat", map[string]string{"f.txt": "one\ntwo\nthree\n"}, "back-merge")
	r.commit("trim", map[string]string{"f.txt": "one\n"})

	for name, tc := range map[string]struct {
		match func([]string) bool
		code  int
	}{
		"tip":              {word("rev-parse", "main"), 1},
		"merge list":       {word("rev-list", "--merges"), 1},
		"label subject":    {word("log", "--format=%s"), 1},
		"label hash":       {word("log", "--format=%h"), 1},
		"ancestry no git":  {func(a []string) bool { return a[0] == "merge-base" }, 127},
		"first parent":     {lastEnds("rev-parse", "^1"), 1},
		"path list":        {word("diff", "--name-only"), 1},
		"history of path":  {word("log", "--oneline"), 1},
		"added lines":      {word("diff", "--"), 1},
		"show no git":      {func(a []string) bool { return a[0] == "show" }, 127},
		"existence no git": {func(a []string) bool { return a[0] == "cat-file" }, 127},
	} {
		_, err := New(failing(r, tc.match, tc.code)).Present(context.Background(), "main", "base")

		require.Error(t, err, name)
	}
}
