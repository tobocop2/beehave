package landing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

const (
	treeA = "1111111111111111111111111111111111111111"
	treeB = "2222222222222222222222222222222222222222"
)

type scriptedExec struct {
	results []model.CommandResult
	calls   [][]string
}

func (s *scriptedExec) Run(_ context.Context, args []string) model.CommandResult {
	s.calls = append(s.calls, args)
	res := s.results[0]
	s.results = s.results[1:]

	return res
}

func scripted(results ...model.CommandResult) (*scriptedExec, GitCLI) {
	s := &scriptedExec{results: results}

	return s, GitCLI{Exec: s}
}

func TestCheckGit_VersionTable(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"git version 2.50.0":                  "",
		"git version 2.38.0":                  "",
		"git version 2.39.5 (Apple Git-154)":  "",
		"git version 3.0.1":                   "",
		"git version 2.37.9":                  "git 2.38 or newer is needed for merge; found 2.37. Upgrade git on the gate host",
		"git version 1.9.4":                   "git 2.38 or newer is needed for merge; found 1.9. Upgrade git on the gate host",
		"git version 2.30.2.windows.1":        "git 2.38 or newer is needed for merge; found 2.30. Upgrade git on the gate host",
		"something else":                      `git 2.38 or newer is needed for merge; git --version printed "something else". Upgrade git on the gate host`,
		"git version 2.9":                     "git 2.38 or newer is needed for merge; found 2.9. Upgrade git on the gate host",
		"git version 10.0":                    "",
		"git version 2.1234567.0 (future gh)": "",
	}

	for text, want := range cases {
		err := CheckGit(context.Background(), versionText{text: text})
		if want == "" {
			require.NoError(t, err, text)
		} else {
			require.EqualError(t, err, want, text)
		}
	}
}

func TestCheckGit_VersionCallFails(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, CheckGit(context.Background(), versionText{err: errBoom}), errBoom)
}

// errBoom is the failure of a fake git call.
var errBoom = errors.New("boom")

// versionText is a git that answers the version call with text, or fails with err.
type versionText struct {
	text string
	err  error
}

func (v versionText) Version(context.Context) (string, error) { return v.text, v.err }

func TestGitCLI_Version(t *testing.T) {
	t.Parallel()

	s, g := scripted(model.CommandResult{Stdout: "git version 2.50.0\n"}, model.CommandResult{ExitCode: 127, Stderr: "git: command not found\n"})

	got, err := g.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "git version 2.50.0", got)
	assert.Equal(t, []string{"--version"}, s.calls[0])

	_, err = g.Version(context.Background())
	require.EqualError(t, err, "git --version failed with exit code 127: git: command not found")
}

func TestGitCLI_MergeTree(t *testing.T) {
	t.Parallel()

	s, g := scripted(
		model.CommandResult{Stdout: treeA + "\n"},
		model.CommandResult{ExitCode: 1, Stdout: treeB + "\n\nCONFLICT (content): Merge conflict in f\n"},
		model.CommandResult{ExitCode: 128, Stderr: "fatal: not a commit\n"},
		model.CommandResult{Stdout: "garbage\n"},
	)
	ctx := context.Background()

	clean, err := g.MergeTree(ctx, "ours", "theirs")
	require.NoError(t, err)
	assert.Equal(t, MergeResult{Tree: treeA}, clean)
	assert.Equal(t, []string{"merge-tree", "--write-tree", "ours", "theirs"}, s.calls[0])

	clash, err := g.MergeTree(ctx, "ours", "theirs")
	require.NoError(t, err)
	assert.Equal(t, MergeResult{Tree: treeB, Conflict: true}, clash)

	_, err = g.MergeTree(ctx, "ours", "theirs")
	require.EqualError(t, err, "git merge-tree failed with exit code 128: fatal: not a commit")

	_, err = g.MergeTree(ctx, "ours", "theirs")
	require.EqualError(t, err, `git merge-tree printed "garbage", not a tree id`)
}

func TestGitCLI_NewCommit(t *testing.T) {
	t.Parallel()

	s, g := scripted(model.CommandResult{Stdout: treeA + "\n"}, model.CommandResult{ExitCode: 128, Stderr: "fatal: bad tree\n"})

	got, err := g.NewCommit(context.Background(), treeB, "p1", "p2")
	require.NoError(t, err)
	assert.Equal(t, treeA, got)
	assert.Equal(t, []string{"commit-tree", treeB, "-m", "beehest batch union", "-p", "p1", "-p", "p2"}, s.calls[0])

	_, err = g.NewCommit(context.Background(), treeB)
	require.EqualError(t, err, "git commit-tree failed with exit code 128: fatal: bad tree")
	assert.NotContains(t, strings.Join(s.calls[1], " "), "-p")
}

func TestGitCLI_HasCommit(t *testing.T) {
	t.Parallel()

	s, g := scripted(
		model.CommandResult{},
		model.CommandResult{ExitCode: 1},
		model.CommandResult{ExitCode: 128, Stderr: "fatal: Not a valid object name\n"},
		model.CommandResult{ExitCode: 2, Stderr: "boom\n"},
	)
	ctx := context.Background()

	found, err := g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []string{"cat-file", "-e", "abc^{commit}"}, s.calls[0])

	found, err = g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.False(t, found)

	found, err = g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.False(t, found)

	_, err = g.HasCommit(ctx, "abc")
	require.EqualError(t, err, "git cat-file failed with exit code 2: boom")
}
