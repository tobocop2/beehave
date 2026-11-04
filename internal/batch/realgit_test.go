package batch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// gitIn runs the real git in one repository with a fixed identity and no user configuration.
type gitIn struct {
	binary string
	dir    string
	home   string
}

func (g gitIn) Run(ctx context.Context, args []string) model.CommandResult {
	cmd := exec.CommandContext(ctx, g.binary, args...) //nolint:gosec // the binary is the git found on PATH and the arguments come from this test
	cmd.Dir = g.dir
	cmd.Env = []string{
		"HOME=" + g.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}

	var out, errOut strings.Builder

	cmd.Stdout, cmd.Stderr = &out, &errOut

	code := 0

	var exit *exec.ExitError

	if err := cmd.Run(); err != nil {
		code = 1
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}

	return model.CommandResult{ExitCode: code, Stdout: out.String(), Stderr: errOut.String()}
}

func (g gitIn) must(t *testing.T, args ...string) string {
	t.Helper()

	res := g.Run(context.Background(), args)
	require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)

	return strings.TrimSpace(res.Stdout)
}

func (g gitIn) write(t *testing.T, name, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(g.dir, name), []byte(text), 0o600))
}

// commitOn switches to a new branch off main, writes one file, commits, and returns the head.
func (g gitIn) commitOn(t *testing.T, branch, file, text string) string {
	t.Helper()
	g.must(t, "switch", "-q", "main")
	g.must(t, "switch", "-q", "-c", branch)
	g.write(t, file, text)
	g.must(t, "add", file)
	g.must(t, "commit", "-q", "-m", branch)

	return g.must(t, "rev-parse", "HEAD")
}

// newRepo makes a temporary repository with one commit on main.
func newRepo(t *testing.T) gitIn {
	t.Helper()

	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	g := gitIn{binary: binary, dir: t.TempDir(), home: t.TempDir()}
	g.must(t, "init", "-q", "-b", "main")
	g.write(t, "README", "one\ntwo\nthree\n")
	g.must(t, "add", "README")
	g.must(t, "commit", "-q", "-m", "base")

	return g
}

type fixedGate struct{ seen []GateInput }

func (f *fixedGate) Run(_ context.Context, in GateInput) (GateOutcome, error) {
	f.seen = append(f.seen, in)

	return GateOutcome{Tree: in.Tree, Passed: true}, nil
}

type fixedBase struct {
	tip    Tip
	merged []string
	prefix []string
}

func (f *fixedBase) Tip(context.Context) (Tip, error) { return f.tip, nil }

func (f *fixedBase) Merge(_ context.Context, m Member, prefixTree string) (MergeOutcome, error) {
	f.merged = append(f.merged, m.Item)
	f.prefix = append(f.prefix, prefixTree)
	f.tip.Tree = prefixTree

	return MergeOutcome{Status: StatusMerged}, nil
}

// TestRun_RealGitFoldsThreeBranchesAndRejectsTheConflictingOne uses the real git on a temporary repository.
// x and y add files, z edits a line that main edited too.
func TestRun_RealGitFoldsThreeBranchesAndRejectsTheConflictingOne(t *testing.T) {
	t.Parallel()

	g := newRepo(t)

	x := g.commitOn(t, "x", "x.txt", "x\n")
	y := g.commitOn(t, "y", "y.txt", "y\n")
	z := g.commitOn(t, "z", "README", "one\nfrom z\nthree\n")

	g.must(t, "switch", "-q", "main")
	g.write(t, "README", "one\nfrom main\nthree\n")
	g.must(t, "commit", "-q", "-a", "-m", "main edit")

	tip := Tip{Commit: g.must(t, "rev-parse", "HEAD"), Tree: g.must(t, "rev-parse", "HEAD^{tree}")}
	gate, base := &fixedGate{}, &fixedBase{tip: tip}
	deps := Deps{Git: GitCLI{Exec: g}, Gate: gate, Base: base, Steps: newMemory(), Name: "real"}

	res, err := Run(context.Background(), deps, []Member{{Item: "z", Head: z}, {Item: "y", Head: y}, {Item: "x", Head: x}})

	require.NoError(t, err)
	assert.Equal(t, []string{"x", "y"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "z", Reason: ReasonConflict}}, res.Rejected)
	assert.Equal(t, 1, res.Gates)
	require.Len(t, gate.seen, 1)

	unionTree := g.must(t, "rev-parse", gate.seen[0].Commit+"^{tree}")
	assert.Equal(t, unionTree, gate.seen[0].Tree)
	assert.Equal(t, "one\nfrom main\nthree", g.must(t, "show", gate.seen[0].Commit+":README"))
	assert.Equal(t, "x", g.must(t, "show", gate.seen[0].Commit+":x.txt"))
	assert.Equal(t, "y", g.must(t, "show", gate.seen[0].Commit+":y.txt"))
	assert.Equal(t, unionTree, base.prefix[1])
	assert.NotEqual(t, tip.Tree, base.prefix[0])
}

func TestCheckGit_InstalledGitPasses(t *testing.T) {
	t.Parallel()

	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	g := gitIn{binary: binary, dir: t.TempDir(), home: t.TempDir()}

	require.NoError(t, CheckGit(context.Background(), GitCLI{Exec: g}))
}
