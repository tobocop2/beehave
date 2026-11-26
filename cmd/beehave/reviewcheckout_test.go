package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

func TestSandboxPlan_ReadOnlyStepDeniesWriteOfTheWorkFolder(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox, req.NoEdits = &model.Sandbox{}, true

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	plan := r.f.spec.Sandbox
	require.NotNil(t, plan)
	assert.Equal(t, plan.Work, r.f.spec.Dir, "the sandbox opens the folder that the step runs in")
	assert.Contains(t, plan.DenyWrite, plan.Work, "a write of the work folder fails at the write")

	edits := request()
	edits.Sandbox = &model.Sandbox{}

	_, err = r.f.agents.Run(t.Context(), edits)
	require.NoError(t, err)
	assert.NotContains(t, r.f.spec.Sandbox.DenyWrite, r.f.spec.Sandbox.Work, "control: a step with edits may write its work folder")
}

func TestWorktrees_ReviewCheckoutIsFreshAtThePin(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	pin := gitOut(t, r.work, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "stray.txt"), []byte("left by an earlier step"), 0o600))

	var seen, head string

	var stray bool

	r.f.agents.run = func(_ context.Context, spec agent.Spec) agent.Result {
		seen, head = spec.Dir, gitOut(t, spec.Dir, "rev-parse", "HEAD")
		_, err := os.Stat(filepath.Join(spec.Dir, "stray.txt"))
		stray = err == nil

		return agent.Result{Outcome: agent.OutcomeExited}
	}

	req := request()
	req.NoEdits = true

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	assert.NotEqual(t, r.work, seen, "the step runs in a checkout, not in the worktree of the item")
	assert.Equal(t, pin, head, "the checkout is at the pin")
	assert.False(t, stray, "the checkout holds no file that an earlier step left")
	assert.NoDirExists(t, seen, "the checkout is discarded when the step ends")
	assert.NotContains(t, gitOut(t, r.work, "worktree", "list", "--porcelain"), seen, "git keeps no record of the checkout")
}

func TestAgents_ReviewCheckoutHasTheSamePathOnARerun(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	var dirs []string

	r.f.agents.run = func(_ context.Context, spec agent.Spec) agent.Result {
		dirs = append(dirs, spec.Dir)
		require.NoError(t, os.WriteFile(filepath.Join(spec.Dir, "left.txt"), []byte("a first run"), 0o600))

		return agent.Result{Outcome: agent.OutcomeExited}
	}

	run := func(item, call string) {
		req := request()
		req.NoEdits, req.Item, req.Call = true, item, call

		_, err := r.f.agents.Run(t.Context(), req)
		require.NoError(t, err)
	}

	run("1", "agent:review#1")
	run("1", "agent:review#1")
	run("1", "agent:other#1")
	run("2", "agent:review#1")

	require.Len(t, dirs, 4)
	assert.Equal(t, dirs[0], dirs[1], "a rerun of one step of one item works in the same folder, so a resumed session finds its working directory")
	assert.NotEqual(t, dirs[0], dirs[2], "another step gets another folder")
	assert.NotEqual(t, dirs[0], dirs[3], "another item gets another folder")
	assert.NoDirExists(t, dirs[0], "the folder is discarded when the step ends")
}

func TestWorktrees_ReviewCheckoutOfAFolderWithNoCommitStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newFolderRig(t)
	runGit(t, r.work, "init", "-q")

	req := request()
	req.NoEdits = true

	_, err := r.f.agents.Run(t.Context(), req)

	require.ErrorContains(t, err, "make the checkout of the review")
}

func TestWorktrees_ReviewCheckoutKeepsAStepWithEditsAndAFolderWithNoRepositoryInTheWorkFolder(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, r.work, r.f.spec.Dir, "a step with edits works in the worktree of the item")

	plain := newFolderRig(t)
	req.NoEdits = true

	_, err = plain.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, plain.work, plain.f.spec.Dir, "a folder with no .git has no commit to check out")
}

func TestWorktrees_ReviewCheckoutStopsTheStepWhenAnotherUserCanWriteTheFolderOfTheCheckouts(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	open := filepath.Join(t.TempDir(), "open")
	require.NoError(t, os.Mkdir(open, 0o700))
	chmodTest(t, open, 0o777)
	r.f.agents.checkouts = open

	req := request()
	req.NoEdits = true

	_, err := r.f.agents.Run(t.Context(), req)

	require.ErrorContains(t, err, "can be written by another user")
}

func TestWorktrees_ReviewCheckoutStopsTheStepWhenTheFolderOfTheCheckoutsCannotBeMade(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	r.f.agents.checkouts = filepath.Join(blocker, "checkouts")

	req := request()
	req.NoEdits = true

	_, err := r.f.agents.Run(t.Context(), req)

	require.ErrorContains(t, err, "make the folder for the checkout of the review")
}
