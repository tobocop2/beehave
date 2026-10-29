package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// repoGit runs the real git in one repository with a fixed identity and no user configuration.
type repoGit struct {
	binary string
	dir    string
	home   string
}

func (g repoGit) Run(ctx context.Context, args []string) model.CommandResult {
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

func (g repoGit) must(t *testing.T, args ...string) string {
	t.Helper()

	res := g.Run(t.Context(), args)
	require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)

	return strings.TrimSpace(res.Stdout)
}

// branchWith makes a branch off main with one file and returns its head.
func (g repoGit) branchWith(t *testing.T, branch, file string) string {
	t.Helper()

	g.must(t, "switch", "-q", "main")
	g.must(t, "switch", "-q", "-c", branch)
	require.NoError(t, os.WriteFile(filepath.Join(g.dir, file), []byte(branch+"\n"), 0o600))
	g.must(t, "add", file)
	g.must(t, "commit", "-q", "-m", branch)

	return g.must(t, "rev-parse", "HEAD")
}

// newBatchRepo makes a repository with main and three member branches. The branch of item 2 adds bad.txt, which the test gate rejects.
func newBatchRepo(t *testing.T) (repo repoGit, heads map[string]string) {
	t.Helper()

	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	repo = repoGit{binary: binary, dir: t.TempDir(), home: t.TempDir()}
	repo.must(t, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo.dir, "README"), []byte("base\n"), 0o600))
	repo.must(t, "add", "README")
	repo.must(t, "commit", "-q", "-m", "base")

	heads = map[string]string{
		"1": repo.branchWith(t, "m1", "one.txt"),
		"2": repo.branchWith(t, "m2", "bad.txt"),
		"3": repo.branchWith(t, "m3", "three.txt"),
	}
	repo.must(t, "switch", "-q", "main")

	return repo, heads
}

// repoHost is a batch host over a real repository. The base is the branch main of the repository.
type repoHost struct {
	repo repoGit

	mu        sync.Mutex
	tipErrs   []error // each Tip call takes the first one; a nil entry is a good call
	tipCalls  int
	intents   []string
	checkouts []string
	intentErr error
	notOpen   map[string]bool // items whose pull request is not OPEN
	// trees is set by a test that needs the folder of the item at the first git call: folderAtGit holds it.
	trees       *fakeTrees
	folderAtGit string
	gitErr      error
	checkErr    error
}

func (h *repoHost) Git(context.Context, string, string) (batch.Git, error) {
	if h.trees != nil {
		h.mu.Lock()
		h.folderAtGit = h.trees.late.path()
		h.mu.Unlock()
	}

	return batch.GitCLI{Exec: h.repo}, h.gitErr
}

func (h *repoHost) Checkout(ctx context.Context, _, _, commit string) (string, error) {
	h.mu.Lock()
	h.checkouts = append(h.checkouts, commit)
	h.mu.Unlock()

	if h.checkErr != nil {
		return "", h.checkErr
	}

	if res := h.repo.Run(ctx, []string{"checkout", "-q", "--detach", commit}); res.ExitCode != 0 {
		return "", errors.New(res.Stderr)
	}

	return strings.TrimSpace(h.repo.Run(ctx, []string{"rev-parse", "HEAD^{tree}"}).Stdout), nil
}

func (h *repoHost) checkoutCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.checkouts)
}

func (h *repoHost) Tip(ctx context.Context, _, _, _ string) (batch.Tip, error) {
	h.mu.Lock()
	h.tipCalls++

	var failure error
	if len(h.tipErrs) > 0 {
		failure, h.tipErrs = h.tipErrs[0], h.tipErrs[1:]
	}
	h.mu.Unlock()

	if failure != nil {
		return batch.Tip{}, failure
	}

	return batch.Tip{
		Commit: strings.TrimSpace(h.repo.Run(ctx, []string{"rev-parse", "main"}).Stdout),
		Tree:   strings.TrimSpace(h.repo.Run(ctx, []string{"rev-parse", "main^{tree}"}).Stdout),
	}, nil
}

func (h *repoHost) Intent(_ context.Context, _, item, _, base, tipTree string, now time.Time) (merge.Intent, error) {
	h.mu.Lock()
	h.intents = append(h.intents, item)
	shut := h.notOpen[item]
	h.mu.Unlock()

	if shut {
		return merge.Intent{}, fmt.Errorf("the pull request of item %s is MERGED, %w, so a merge cannot be tied to this run", item, ErrMemberNotOpen)
	}

	return merge.Intent{PR: 1, Head: item, Base: base, BaseTipTree: tipTree, State: "OPEN", Time: now}, h.intentErr
}

func (h *repoHost) tipReads() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.tipCalls
}

// treeGate is the gate of the test: it passes the checked-out tree when bad.txt is not in it. The tree it returns is the tree of HEAD.
type treeGate struct {
	repo  repoGit
	mu    sync.Mutex
	trees []string
	// lie, when set, is the tree that the gate reports in place of the tree it ran on.
	lie string
}

func (g *treeGate) Run(ctx context.Context, _, _ string) (GateResult, error) {
	tree := strings.TrimSpace(g.repo.Run(ctx, []string{"rev-parse", "HEAD^{tree}"}).Stdout)
	head := strings.TrimSpace(g.repo.Run(ctx, []string{"rev-parse", "HEAD"}).Stdout)
	passed := g.repo.Run(ctx, []string{"cat-file", "-e", "HEAD:bad.txt"}).ExitCode != 0

	g.mu.Lock()
	g.trees = append(g.trees, tree)
	g.mu.Unlock()

	g.mu.Lock()
	lie := g.lie
	g.mu.Unlock()

	if lie != "" {
		tree = lie
	}

	return GateResult{Passed: passed, Report: "gate on " + tree, Head: head, Tree: tree}, nil
}

func (g *treeGate) ran() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.trees...)
}

// landMerger lands a member the way the forge does: main moves to a commit of the gated tree. script[item] answers first when it has an entry.
type landMerger struct {
	repo repoGit

	mu     sync.Mutex
	asked  []string
	given  []MergeSettings
	script map[string][]merge.Result
	err    error
	// after runs once a member has landed.
	after func(item string)
	// onceOnly makes a second call for a member that already landed return Done with no second landing, as merge.Run does with the intent.
	onceOnly bool
	done     map[string]bool
	// cancelOn names the member after whose landing the run is canceled, once; cancel does it.
	cancelOn string
	canceled bool
	cancel   func()
}

func (m *landMerger) Merge(ctx context.Context, _, item, _ string, settings MergeSettings) (merge.Result, error) {
	m.mu.Lock()
	m.asked = append(m.asked, item)
	m.given = append(m.given, settings)

	if queue := m.script[item]; len(queue) > 0 {
		answer := queue[0]
		m.script[item] = queue[1:]
		m.mu.Unlock()

		return answer, m.err
	}

	hook, replay := m.after, m.onceOnly && m.done[item]

	if m.onceOnly {
		if m.done == nil {
			m.done = map[string]bool{}
		}

		m.done[item] = true
	}

	m.mu.Unlock()

	if m.err != nil {
		return merge.Result{}, m.err
	}

	if replay {
		return merge.Result{Stop: merge.Done}, nil
	}

	tip := strings.TrimSpace(m.repo.Run(ctx, []string{"rev-parse", "main"}).Stdout)
	land := strings.TrimSpace(m.repo.Run(ctx, []string{"commit-tree", settings.GatedTree, "-p", tip, "-m", "land " + item}).Stdout)
	m.repo.Run(ctx, []string{"update-ref", "refs/heads/main", land})

	if hook != nil {
		hook(item)
	}

	m.cancelHere(ctx, item)

	return merge.Result{Stop: merge.Done}, nil
}

// cancelHere cancels the run once, right after the member cancelOn landed, and waits for the step to see it. The forge has merged and the step has not returned.
func (m *landMerger) cancelHere(ctx context.Context, item string) {
	m.mu.Lock()
	cancel := m.cancelOn == item && !m.canceled
	m.canceled = m.canceled || cancel
	m.mu.Unlock()

	if cancel {
		go m.cancel()

		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
	}
}

func (m *landMerger) landed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.asked...)
}
