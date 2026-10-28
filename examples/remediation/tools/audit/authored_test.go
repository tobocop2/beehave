package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func authoredLines(t *testing.T, r *repo, span string) []string {
	t.Helper()

	lines, err := New(r).Authored(context.Background(), span)
	require.NoError(t, err)

	return texts(lines)
}

func TestIsSensitive_Boundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path      string
		sensitive bool
	}{
		{".github/workflows/ci.yml", true},
		{".githubx/ci.yml", false},
		{"github/ci.yml", false},
		{"a/.github/ci.yml", false},
		{"scripts/run.sh", true},
		{"script/run.sh", false},
		{"scriptsx/run.sh", false},
		{"sql/a.sql", true},
		{"sqlx/a.sql", false},
		{"uv.lock", true},
		{"deep/dir/poetry.lock", true},
		{"lock", false},
		{"a.lockx", false},
		{"pin/readme.md", false},
		{"src/requirements-pin.txt", true},
		{"pinned.txt", true},
		{"PIN.txt", false},
		{"a/baseline.json", true},
		{"allowlist.txt", true},
		{"allow-list.txt", true},
		{"allow_list.txt", false},
		{"lockfile.json", true},
		{"gate.sh", true},
		{"my.secrets.yml", true},
		{"census.csv", true},
		{"cens.csv", false},
		{"plain.txt", false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.sensitive, IsSensitive(tc.path), tc.path)
	}
}

func TestAuthored_RangeWithNoMergePrintsOnlyTheTotals(t *testing.T) {
	t.Parallel()

	r := newRepo(t)

	assert.Equal(t, []string{"TOTAL_AUTHORED_PATHS=0", "AUD_AUTHORED_DONE"}, authoredLines(t, r, "main"))
}

func TestAuthored_PathTheMergeAddedIsAuthoredInNeitherParent(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	merged := r.merge("side", "merge side", map[string]string{"scripts/new.sh": "echo\n", "plain.txt": "p\n"})

	got := authoredLines(t, r, "main")

	assert.Equal(t, strings.Join([]string{
		r.short(merged) + " differs_from_all_parents=2 resolved=0 AUTHORED=2 | merge side",
		"      AUTHORED (in neither parent, added by the merge): plain.txt",
		"      AUTHORED (in neither parent, added by the merge): scripts/new.sh  <== sensitive path",
	}, "\n"), got[0])
	assert.Equal(t, []string{"TOTAL_AUTHORED_PATHS=2", "AUD_AUTHORED_DONE"}, got[1:])
}

func TestAuthored_PathIdenticalInBothParentsAndChangedByTheMergeIsAuthored(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	r.merge("side", "merge side", map[string]string{"x.txt": "changed\n"})

	got := authoredLines(t, r, "main")

	assert.Contains(t, got[0], "AUTHORED (identical in both parents, changed by the merge): x.txt")
	assert.Contains(t, got[0], "AUTHORED=1")
}

func TestAuthored_ConflictTheMergeResolvedIsResolvedNotAuthored(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.branch("side")
	r.commit("side edit", map[string]string{"shared.txt": "side\n"})
	r.checkout("main")
	r.commit("main edit", map[string]string{"shared.txt": "main\n"})
	merged := r.merge("side", "resolve", map[string]string{"shared.txt": "both\n"})

	got := authoredLines(t, r, "main")

	assert.Equal(t, r.short(merged)+" differs_from_all_parents=1 resolved=1 AUTHORED=0 | resolve", got[0])
	assert.Equal(t, "TOTAL_AUTHORED_PATHS=0", got[1])
}

func TestAuthored_NonASCIIConflictTheMergeResolvedIsResolvedNotAuthored(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.branch("side")
	r.commit("side edit", map[string]string{"é.txt": "side\n"})
	r.checkout("main")
	r.commit("main edit", map[string]string{"é.txt": "main\n"})
	r.merge("side", "resolve", map[string]string{"é.txt": "both\n"})

	assert.Contains(t, authoredLines(t, r, "main")[0], "differs_from_all_parents=1 resolved=1 AUTHORED=0")
}

func TestAuthored_NonASCIIAndSpacePathsKeepTheirNameAndSensitiveMark(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	r.merge("side", "merge side", map[string]string{"scripts/é.sh": "echo\n", "é.lock": "x\n", "a b/c d.txt": "s\n"})

	got := authoredLines(t, r, "main")[0]

	assert.Contains(t, got, "added by the merge): scripts/é.sh  <== sensitive path")
	assert.Contains(t, got, "added by the merge): é.lock  <== sensitive path")
	assert.Contains(t, got, "added by the merge): a b/c d.txt\n")
}

func TestAuthored_CleanMergePrintsNoLine(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	r.merge("side", "clean", nil)

	assert.Equal(t, []string{"TOTAL_AUTHORED_PATHS=0", "AUD_AUTHORED_DONE"}, authoredLines(t, r, "main"))
}

func TestAuthored_TotalAddsUpOverEveryMergeInTheRange(t *testing.T) {
	t.Parallel()

	r := newRepo(t)

	for _, name := range []string{"one", "two"} {
		r.checkout("main")
		r.branch(name)
		r.commit(name+" work", map[string]string{name + ".txt": "s\n"})
		r.checkout("main")
		r.commit("main "+name, map[string]string{"m" + name + ".txt": "m\n"})
		r.merge(name, "merge "+name, map[string]string{"extra-" + name + ".txt": "e\n"})
	}

	got := authoredLines(t, r, "main")

	assert.Len(t, got, 4)
	assert.Equal(t, "TOTAL_AUTHORED_PATHS=2", got[2])
}

func TestAuthored_SubjectIsCutAt58Characters(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ length, cut int }{{57, 57}, {58, 58}, {59, 58}, {80, 58}} {
		r := newRepo(t)
		r.sideAndMain()
		r.merge("side", strings.Repeat("S", tc.length), map[string]string{"extra.txt": "e\n"})

		head := strings.Split(authoredLines(t, r, "main")[0], "\n")[0]

		assert.True(t, strings.HasSuffix(head, " | "+strings.Repeat("S", tc.cut)), tc.length)
	}
}

func TestAuthored_SubjectWithANonASCIICharacterIsCutByCharacters(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	r.merge("side", strings.Repeat("é", 60), map[string]string{"extra.txt": "e\n"})

	head := strings.Split(authoredLines(t, r, "main")[0], "\n")[0]

	assert.True(t, strings.HasSuffix(head, " | "+strings.Repeat("é", 58)))
}

func TestAuthored_RangeGitCannotReadIsARefusalNotACleanAnswer(t *testing.T) {
	t.Parallel()

	r := newRepo(t)

	_, err := New(r).Authored(context.Background(), "nonsense")

	require.ErrorContains(t, err, "git rev-list --merges nonsense failed")
}

func TestAuthored_MissingGitIsARefusal(t *testing.T) {
	t.Parallel()

	_, err := New(runnerFunc(func([]string) model.CommandResult { return model.CommandResult{ExitCode: 127} })).Authored(context.Background(), "main")

	require.ErrorIs(t, err, ErrNoGit)
}

// runnerFunc answers every call from one function.
type runnerFunc func(args []string) model.CommandResult

func (f runnerFunc) Run(_ context.Context, args []string) model.CommandResult { return f(args) }

// failing runs the real git except for the calls that match, which fail with the code.
func failing(r *repo, match func(args []string) bool, code int) runnerFunc {
	return func(args []string) model.CommandResult {
		if match(args) {
			return model.CommandResult{ExitCode: code}
		}

		return r.Run(context.Background(), args)
	}
}

func TestAuthored_EveryReadThatMustSucceedRefusesWhenItFails(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.sideAndMain()
	r.merge("side", "merge side", map[string]string{"plain.txt": "p\n"})

	for name, tc := range map[string]struct {
		match func([]string) bool
		code  int
	}{
		"merge list":         {word("rev-list", "--merges"), 1},
		"merge label":        {word("log", "--format=%h"), 1},
		"merge subject":      {word("log", "--format=%s"), 1},
		"first parent":       {lastEnds("rev-parse", "^1"), 1},
		"second parent":      {lastEnds("rev-parse", "^2"), 1},
		"path list":          {word("diff-tree", "-z"), 1},
		"blob without git":   {nth(1, blobRead), 127},
		"blob of the second": {nth(2, blobRead), 127},
		"merge list no git":  {word("rev-list", "--merges"), 127},
		"merge label no git": {word("log", "--format=%h"), 127},
	} {
		_, err := New(failing(r, tc.match, tc.code)).Authored(context.Background(), "main")

		require.Error(t, err, name)
	}
}

func blobRead(args []string) bool {
	return args[0] == "rev-parse" && strings.Contains(args[len(args)-1], ":")
}

// nth matches only the nth call (from 1) that the match accepts.
func nth(n int, match func([]string) bool) func([]string) bool {
	seen := 0

	return func(args []string) bool {
		if !match(args) {
			return false
		}

		seen++

		return seen == n
	}
}

func word(sub, w string) func([]string) bool {
	return func(args []string) bool {
		return args[0] == sub && contains(args, w)
	}
}

func lastEnds(sub, suffix string) func([]string) bool {
	return func(args []string) bool {
		return args[0] == sub && strings.HasSuffix(args[len(args)-1], suffix)
	}
}

func contains(args []string, w string) bool {
	for _, a := range args {
		if a == w {
			return true
		}
	}

	return false
}

func TestOpen_FolderThatDoesNotExistIsNoWorktree(t *testing.T) {
	t.Parallel()

	_, err := Open(runnerFunc(nil), "/nonexistent/folder/for/audit")

	var none NoWorktree

	require.ErrorAs(t, err, &none)
	assert.Equal(t, 9, none.Code())
	assert.Equal(t, "cannot enter /nonexistent/folder/for/audit", err.Error())
}

func TestOpen_FolderThatExistsOpens(t *testing.T) {
	t.Parallel()

	r := newRepo(t)

	got, err := Open(r, r.dir)

	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestLine_StringIsTheText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "x", Line("x").String())
}
