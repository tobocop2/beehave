package flow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/proc"
)

// A promisor remote in the clone config and a HEAD whose tree only the promisor holds make a plain git status fetch the tree. The release reads the
// worktree with local git calls: it fetches nothing, runs no ssh command of the clone config and keeps the tree. The control is a plain git status in the
// worktree, which runs the ssh command or takes the tree from the promisor.
func TestRemoveTree_FetchesNoMissingObjectFromAPromisorThatTheCloneConfigNames(t *testing.T) {
	t.Parallel()

	for _, ssh := range []bool{true, false} {
		name := "a file promisor remote that holds the tree"
		if ssh {
			name = "an ssh promisor remote"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRepos(t)
			promisor := filepath.Join(t.TempDir(), "promisor")
			require.NoError(t, os.Mkdir(promisor, 0o700))
			gitIn(t, promisor, "init", "-b", "main")
			commitIn(t, promisor, "only-"+strings.ReplaceAll(t.Name(), "/", "-"))
			tree := gitIn(t, promisor, "rev-parse", "HEAD^{tree}")

			literal := exec.CommandContext(t.Context(), "git", "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
			literal.Dir = r.tree
			literal.Stdin = strings.NewReader("tree " + tree + "\nauthor t <t@example.com> 0 +0000\ncommitter t <t@example.com> 0 +0000\n\nmissing tree\n")
			head, err := literal.Output()
			require.NoError(t, err)
			gitIn(t, r.tree, "update-ref", "HEAD", strings.TrimSpace(string(head)))

			ran := filepath.Join(t.TempDir(), "ran")
			marker := filepath.Join(t.TempDir(), "marker")
			require.NoError(t, exectest.Write(marker, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))

			gitIn(t, r.main, "config", "core.repositoryformatversion", "1")
			gitIn(t, r.main, "config", "extensions.partialClone", "origin")
			gitIn(t, r.main, "config", "remote.origin.promisor", "true")

			if ssh {
				gitIn(t, r.main, "config", "remote.origin.url", "ssh://example.invalid/promisor.git")
				gitIn(t, r.main, "config", "core.sshCommand", marker)
			} else {
				gitIn(t, r.main, "config", "remote.origin.url", promisor)
			}

			kept, err := removeTreeOf(t, r.tree, 0)

			require.NoError(t, err)
			assert.Contains(t, kept, "git could not read the state of the worktree", "the release keeps the tree whose HEAD it cannot read")
			assert.DirExists(t, r.tree)
			assert.NoFileExists(t, ran, "the release runs no ssh command of the clone config")
			assert.False(t, holdsObject(t, r.tree, tree), "the release took no object from the promisor")

			control := exec.CommandContext(t.Context(), "git", "status", "--porcelain")
			control.Dir = r.tree
			out, _ := control.CombinedOutput() //nolint:errcheck // the control shows the effect of the promisor, whether or not git then fails

			if ssh {
				assert.FileExists(t, ran, "control: a plain git status runs the ssh command of the clone config: %s", out)
			} else {
				assert.True(t, holdsObject(t, r.tree, tree), "control: a plain git status takes the tree from the promisor: %s", out)
			}
		})
	}
}

// holdsObject reports whether the repository of dir has the object, without a fetch from a promisor.
func holdsObject(t *testing.T, dir, object string) bool {
	t.Helper()

	command := proc.GroupCommand(t.Context(), "git", []string{"cat-file", "-e", object}, dir)
	command.Env = append(command.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=file")

	return command.Run() == nil
}
