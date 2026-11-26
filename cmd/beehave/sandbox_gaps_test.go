package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestSandboxPlan_AnAllowWritePatternThatMatchesACredentialPathIsRefused(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	for _, pattern := range []string{"~/.ss*", "~/.s?h"} {
		_, note := r.writeNote(t, pattern)

		assert.Contains(t, note, "The step did not start", pattern)
	}
}

func TestSandboxPlan_AnAllowWriteLinkToAMissingTargetInsideAClosedPathIsRefused(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(r.work, ".git", "hooks"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(r.work, ".git", "hooks", "pre-commit"), filepath.Join(r.work, "abs")))
	require.NoError(t, os.Symlink(filepath.Join(".husky", "pre-push"), filepath.Join(r.work, "rel")))

	for _, bad := range []string{"abs", "rel", "rel/more"} {
		_, note := r.writeNote(t, bad)

		assert.Contains(t, note, "overlaps the protected path", bad)
	}
}

func TestSandboxPlan_AGitFolderThatDoesNotExistYetIsDeniedForWrite(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Contains(t, r.f.spec.Sandbox.DenyWrite, filepath.Join(r.work, ".git"))
}

func TestSandboxPlan_ASandboxedStepHasNoDenyOnTheObjectFolderOfTheClone(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	for _, item := range []string{"7", roundItem} {
		t.Run(model.SafeName(item), func(t *testing.T) {
			t.Parallel()

			dir, err := rig.trees.Ensure(t.Context(), "p", item, "", "")
			require.NoError(t, err)

			common := gitOut(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
			require.Equal(t, filepath.Join(laneOf(rig.clone(), item), ".git"), common, "the worktree is linked to the lane of its item")

			r := newSandboxRig(t)
			r.f.agents.trees = rig.trees
			r.f.agents.repos = gitexec.NewRepositories(filepath.Dir(rig.clone()))
			req := request()
			req.Item, req.Sandbox = item, &model.Sandbox{}

			_, err = r.f.agents.Run(t.Context(), req)
			require.NoError(t, err)

			plan := r.f.spec.Sandbox
			assert.Contains(t, plan.GitDirs, common, "the step writes the shared git directory")
			assert.Contains(t, plan.DenyWritePatterns, filepath.Join(plan.Work, ".git/hooks"), "control: the plan holds write denies")

			objects := filepath.Join(common, "objects")
			for _, list := range [][]string{plan.Deny, plan.DenyPatterns, plan.DenyWrite, plan.DenyWritePatterns} {
				for _, entry := range list {
					assert.False(t, entry == common || entry == objects || strings.HasPrefix(entry, objects+"/"), "%s denies the object folder, so git gc fails in the step", entry)
				}
			}
		})
	}
}

func TestSandboxPlan_HooksAndAgentSettingsAreDeniedForWriteByPatternAtAnyDepth(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	got := r.f.spec.Sandbox.DenyWritePatterns

	for _, name := range []string{".git/hooks", ".husky", ".githooks", ".claude/hooks", ".claude/settings*.json"} {
		for _, base := range []string{filepath.Join(r.work, name), filepath.Join(r.work, "**", name)} {
			assert.Contains(t, got, base, name)
			assert.Contains(t, got, base+"/**", name)
		}
	}

	assert.NotContains(t, got, filepath.Join(r.work, "**", ".claude"), "the CLI keeps its state in .claude")
	assert.NotContains(t, r.f.spec.Sandbox.DenyPatterns, filepath.Join(r.work, "**", ".husky"), "hooks stay readable")
}
