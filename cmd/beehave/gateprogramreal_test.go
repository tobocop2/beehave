package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

// realProgramGate returns a gate whose job of a gate_program runs under the enforcer of this machine, in the item repository repo, and the project of its request.
// It skips the test, with the reason, on a machine where no enforcer can run.
func realProgramGate(t *testing.T, repo, head string) (gate.Gate, gate.Request) {
	t.Helper()

	enforcer, err := chooseEnforcer("")
	if err != nil {
		t.Skipf("the job is not run under the real enforcer, because this machine has none: %v", err)
	}

	box := sandboxFor(t, enforcer, nil)
	box.enforcer = chooseEnforcer
	box.environ = model.EnvPairs(config.ProcessEnv())
	box.projects = programGateProject("", []string{"scripts/"})
	box.clones = cloneLock{repos: filepath.Dir(repo)}

	tools, err := resolveGateTools(config.ProcessEnv()["PATH"])
	require.NoError(t, err)

	shell := func(context.Context, string, string) (string, int) { return "the shell of the project gate ran", 1 }
	tree := func(context.Context, string) (gate.State, error) {
		return gate.State{Head: head, Tree: "tree", Clean: true}, nil
	}
	program := func(ctx context.Context, from, root, command string) (string, int) {
		return box.runJob(ctx, "p", from, root, []string{"sh", "-c", command}, 2*time.Minute)
	}

	project := box.projects["p"].Project
	project.Gate = "sh scripts/check.sh"

	return gate.New(shell, tree, nil).WithProgramShell(program).WithTools(tools), gate.Request{Project: project, Dir: repo, BaseSHA: gitHead(t, repo, "main")}
}

// gitHead returns the commit that rev names in repo.
func gitHead(t *testing.T, repo, rev string) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", "rev-parse", rev) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = repo

	out, err := command.Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

// TestGateSandbox_TheJobOfAGateProgramRunsUnderTheRealEnforcer runs the whole job under the enforcer of this machine: the snapshot of the base commit, the refusal, the
// overlay, the gate command and the final compare. A fake enforcer reads no path, so only this test can find a path that the plan leaves out.
func TestGateSandbox_TheJobOfAGateProgramRunsUnderTheRealEnforcer(t *testing.T) { //nolint:paralleltest // the test starts the real enforcer
	repo := repoWithCommit(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "check.sh"), []byte("exit 0\n"), 0o600))
	runGit(t, repo, "add", "scripts")
	runGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")

	runGit(t, repo, "checkout", "-q", "-b", "good")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "code.txt"), []byte("new\n"), 0o600))
	runGit(t, repo, "add", "code.txt")
	runGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "good")

	good := gitHead(t, repo, "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "weak", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "check.sh"), []byte("exit 0 # weaker\n"), 0o600))
	runGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-a", "-m", "weak")

	weak := gitHead(t, repo, "HEAD")

	g, request := realProgramGate(t, repo, good)
	result, err := g.Run(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, result.Passed, "a good head passes under the real enforcer: "+result.Report)

	g, request = realProgramGate(t, repo, weak)
	result, err = g.Run(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, result.Passed, "a head that changed the gate program is refused under the real enforcer")
	assert.Contains(t, result.Report, "the head changed the gate program, paths scripts/check.sh", result.Report)
}
