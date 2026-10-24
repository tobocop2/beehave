package forge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

const branchPoint = "2026-10-01T00:00:00Z"

func otherPR(number int, title string, paths ...string) fields {
	files := []fields{}
	for _, path := range paths {
		files = append(files, fields{"path": path})
	}

	return fields{"number": number, "author": fields{"login": "gz"}, "title": title, "files": files}
}

// overlapRun plans a pull request 5 with two files, no open pull requests and no base commits.
func overlapRun(t *testing.T, listed ...fields) *scripted {
	t.Helper()

	if listed == nil {
		listed = []fields{}
	}

	run := &scripted{}
	run.plan("docs/ab.md\ndocs/a b.md\n", "repos/o/n/pulls/5/files")
	run.plan("2222\n", "repos/o/n/pulls/5")
	run.planContaining(branchPoint+"\n", "compare/")
	run.plan("", "repos/o/n/commits")
	run.plan(toJSON(t, listed), "pr", "list")

	return run
}

func overlap(t *testing.T, run *scripted, req forge.OverlapRequest) (forge.Overlap, error) {
	t.Helper()

	if req.Base == "" {
		req.Base = "main"
	}

	req.Now = now

	return newForge(t, "o/n", run).Overlap(context.Background(), req)
}

func TestOverlap_AnOpenPullRequestOnOneOfOurFilesIsReported(t *testing.T) {
	t.Parallel()

	got, err := overlap(t, overlapRun(t, otherPR(9, "t", "docs/ab.md")), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, 1, got.Found)
	assert.Equal(t, []string{"OPEN PR #9 by gz shares: docs/ab.md (t)", "overlap: 1 finding(s) over 2 file(s)"}, got.Lines)
}

func TestOverlap_SharedFilesAreSortedJoinedAndSpacesStayInTheName(t *testing.T) {
	t.Parallel()

	got, err := overlap(t, overlapRun(t, otherPR(9, "a\tb", "docs/ab.md", "docs/a b.md", "z")), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, "OPEN PR #9 by gz shares: docs/a b.md, docs/ab.md (a\tb)", got.Lines[0])
}

func TestOverlap_APathWithASpaceIsNotSplitIntoWords(t *testing.T) {
	t.Parallel()

	run := overlapRun(t, otherPR(9, "t", "docs/a", "b.md"))
	run.plan("docs/a b.md\n", "repos/o/n/pulls/5/files")

	got, err := overlap(t, run, forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, []string{"overlap: 0 finding(s) over 1 file(s)"}, got.Lines)

	var history []string

	for _, call := range run.calls {
		if strings.Contains(strings.Join(call, " "), "repos/o/n/commits") {
			history = call
		}
	}

	assert.Equal(t, []string{
		"api", "-X", "GET", "repos/o/n/commits", "-f", "sha=main", "-f", "path=docs/a b.md", "-f", "since=" + branchPoint,
		"--jq", `.[] | "\(.sha[0:9]) \(.commit.message | split("\n")[0])"`,
	}, history)
}

func TestOverlap_OurOwnPullRequestInTheOpenListIsNotAnOverlap(t *testing.T) {
	t.Parallel()

	got, err := overlap(t, overlapRun(t, otherPR(5, "t", "docs/ab.md"), otherPR(9, "t", "docs/zz.md")), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Found)
}

func TestOverlap_AHundredFilesAndNoneSharedIsUncheckedNotClean(t *testing.T) {
	t.Parallel()

	for count, want := range map[int]int{100: 1, 99: 0} {
		paths := make([]string, count)
		for i := range paths {
			paths[i] = fmt.Sprintf("big/f%d", i)
		}

		got, err := overlap(t, overlapRun(t, otherPR(9, "t", paths...)), forge.OverlapRequest{PR: "5"})
		require.NoError(t, err)
		assert.Equal(t, want, got.Found, count)

		if want == 1 {
			assert.Equal(t, "UNCHECKED PR #9 by gz has 100 or more files; compare it by hand (t)", got.Lines[0])
		}
	}
}

func TestOverlap_ARowWithoutFilesOrAuthorIsNotAnOverlap(t *testing.T) {
	t.Parallel()

	got, err := overlap(t, overlapRun(t, fields{"number": 9, "title": "t", "files": nil}), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Found)
}

func TestOverlap_AnAuthorlessPullRequestPrintsNull(t *testing.T) {
	t.Parallel()

	row := otherPR(9, "t", "docs/ab.md")
	row["author"] = nil

	got, err := overlap(t, overlapRun(t, row), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, "OPEN PR #9 by null shares: docs/ab.md (t)", got.Lines[0])
}

func TestOverlap_ABaseCommitOnAFileIsReported(t *testing.T) {
	t.Parallel()

	run := overlapRun(t)
	run.plan("abc123def Fix the thing\n123456789 Another\n", "repos/o/n/commits")

	got, err := overlap(t, run, forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Found)
	assert.Equal(t, "ON main since "+branchPoint+", docs/a b.md: abc123def Fix the thing", got.Lines[0])
	assert.Equal(t, "ON main since "+branchPoint+", docs/a b.md: 123456789 Another", got.Lines[1])
	assert.Equal(t, "overlap: 2 finding(s) over 2 file(s)", got.Lines[len(got.Lines)-1])
}

func TestOverlap_Refusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*scripted)
		req  forge.OverlapRequest
		want string
	}{
		{"files_unreadable", func(r *scripted) { r.planFail("", "repos/o/n/pulls/5/files") }, forge.OverlapRequest{PR: "5"}, "cannot read the files of #5"},
		{"head_unreadable", func(r *scripted) { r.planFail("", "repos/o/n/pulls/5") }, forge.OverlapRequest{PR: "5"}, "cannot read the head of #5"},
		{"branch_point_unreadable", func(r *scripted) { r.planContainingFail("compare/") }, forge.OverlapRequest{PR: "5"}, "cannot find where #5 branched from main"},
		{"history_unreadable", func(r *scripted) { r.planFail("", "repos/o/n/commits") }, forge.OverlapRequest{PR: "5"}, "cannot read the history of docs/a b.md on main"},
		{"no_files", func(r *scripted) { r.plan("", "repos/o/n/pulls/5/files") }, forge.OverlapRequest{PR: "5"}, "no files to compare. #5 has no files, and an empty list proves nothing."},
		{"open_list_unreadable", func(r *scripted) { r.planFail("", "pr", "list") }, forge.OverlapRequest{PR: "5"}, "cannot list the open pull requests: cannot list pull requests ("},
		{"no_argument", func(*scripted) {}, forge.OverlapRequest{}, "usage: obee check overlap <pr> | --files <list> [--since <ISO date>]"},
		{"two_branch_names", func(*scripted) {}, forge.OverlapRequest{PR: "5", Integration: "other"}, "BASE is main and INTEGRATION_BRANCH is other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := overlapRun(t)
			tt.edit(run)

			_, err := overlap(t, run, tt.req)

			var refused *forge.Error

			require.ErrorAs(t, err, &refused)
			assert.Equal(t, forge.CodeRefused, refused.Code)
			assert.True(t, strings.HasPrefix(refused.Message, tt.want), refused.Message)
		})
	}
}

func TestOverlap_AListThatFillsTheLimitIsRefusedAndNamesTheCut(t *testing.T) {
	t.Parallel()

	rows := make([]fields, 1000)
	for i := range rows {
		rows[i] = otherPR(100+i, "t", "z")
	}

	_, err := overlap(t, overlapRun(t, rows...), forge.OverlapRequest{PR: "5"})
	require.ErrorContains(t, err, "may be cut off")
	require.ErrorContains(t, err, "cannot list the open pull requests")

	got, err := overlap(t, overlapRun(t, rows[:999]...), forge.OverlapRequest{PR: "5"})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Found)
}

func TestOverlap_WithoutFilesTheHistoryIsNotRead(t *testing.T) {
	t.Parallel()

	run := overlapRun(t, otherPR(9, "t", "docs/ab.md"))
	run.plan("", "repos/o/n/pulls/5/files")

	_, err := overlap(t, run, forge.OverlapRequest{PR: "5"})
	require.Error(t, err)
	assert.False(t, run.called("repos/o/n/commits"))
}

func listFile(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "files.txt")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestOverlap_TheFileListIsSortedUniqueAndReadByteForByte(t *testing.T) {
	t.Parallel()

	run := overlapRun(t)
	req := forge.OverlapRequest{Files: listFile(t, "b.md\na.md\nb.md\n\n"), Since: "2026-09-01T00:00:00Z"}

	got, err := overlap(t, run, req)
	require.NoError(t, err)
	assert.Equal(t, []string{"overlap: 0 finding(s) over 2 file(s)"}, got.Lines)

	var paths []string

	for _, call := range run.calls {
		if strings.Contains(strings.Join(call, " "), "repos/o/n/commits") {
			paths = append(paths, call[7], call[9])
		}
	}

	assert.Equal(t, []string{"path=a.md", "since=2026-09-01T00:00:00Z", "path=b.md", "since=2026-09-01T00:00:00Z"}, paths)
}

func TestOverlap_AReturnStaysInTheNameAndALoneReturnDoesNotSplit(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"crlf":       "a.md\r\nb.md\r\n",
		"lone_cr":    "a.md\rb.md\n",
		"cr_only":    "\r\n",
		"plain_text": "a.md\n",
	}
	for name, text := range tests {
		run := overlapRun(t)

		_, err := overlap(t, run, forge.OverlapRequest{Files: listFile(t, text)})
		require.NoError(t, err, name)

		var seen []string

		for _, call := range run.calls {
			if len(call) > 7 && strings.HasPrefix(call[7], "path=") {
				seen = append(seen, call[7])
			}
		}

		switch name {
		case "crlf":
			assert.Equal(t, []string{"path=a.md\r", "path=b.md\r"}, seen)
		case "lone_cr":
			assert.Equal(t, []string{"path=a.md\rb.md"}, seen)
		case "cr_only":
			assert.Equal(t, []string{"path=\r"}, seen)
		default:
			assert.Equal(t, []string{"path=a.md"}, seen)
		}
	}
}

func TestOverlap_TheDefaultSinceIsSevenDaysBeforeNow(t *testing.T) {
	t.Parallel()

	run := overlapRun(t)

	_, err := overlap(t, run, forge.OverlapRequest{Files: listFile(t, "a\n")})
	require.NoError(t, err)
	assert.True(t, run.called("since=2026-10-16T12:00:00Z"))
}

func TestOverlap_AnUnusableFileListIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.txt")
	unreadable := filepath.Join(dir, "locked.txt")
	require.NoError(t, os.WriteFile(unreadable, []byte("a\n"), 0o000))

	for _, path := range []string{gone, dir, unreadable} {
		_, err := overlap(t, overlapRun(t), forge.OverlapRequest{Files: path})
		require.EqualError(t, err, "no file list at "+path)
	}

	_, err := overlap(t, overlapRun(t), forge.OverlapRequest{Files: listFile(t, "\n\n")})
	require.EqualError(t, err, "no files to compare. An empty list overlaps with nothing and proves nothing.")
}

func TestOverlap_ThePullRequestNumberMustBeDigits(t *testing.T) {
	t.Parallel()

	for _, pr := range []string{"5x", "-5", "5 ", "../5", "a"} {
		run := overlapRun(t)

		_, err := overlap(t, run, forge.OverlapRequest{PR: pr})
		require.EqualError(t, err, fmt.Sprintf("the pull request number must be digits, got %q", pr))
		assert.Empty(t, run.calls)
	}
}
