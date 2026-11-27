package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// promisorRig is a push rig whose clone config names a promisor remote. The worktree has HEAD at a commit whose tree only the promisor holds, and the
// promisor holds a commit that the clone lacks. A plain git read of either object fetches it from the promisor.
type promisorRig struct {
	pushRig

	commit string
	// missing is the tree at HEAD of the worktree, which only the promisor holds.
	missing string
	ran     string
}

// promisorKinds are the promisor remotes that an agent could name in the clone config. An ssh promisor runs the ssh command of the clone config with the
// credential of the owner. A file promisor needs no credential, but its fetch still takes objects from outside the clone.
var promisorKinds = map[string]func(t *testing.T, rig pushRig, promisor, marker string){
	sshPromisor: func(t *testing.T, rig pushRig, _, marker string) {
		t.Helper()

		rig.plant(t, "remote.origin.url", "ssh://example.invalid/promisor.git")
		rig.plant(t, "core.sshCommand", marker)
	},
	"a file promisor remote that holds the object": func(t *testing.T, rig pushRig, promisor, _ string) {
		t.Helper()

		rig.plant(t, "remote.origin.url", promisor)
	},
}

// sshPromisor names the promisor whose fetch runs the ssh command of the clone config.
const sshPromisor = "an ssh promisor remote"

func newPromisorRig(t *testing.T, kind func(t *testing.T, rig pushRig, promisor, marker string)) promisorRig {
	t.Helper()

	rig := newPushRig(t)

	promisor := filepath.Join(t.TempDir(), "promisor")
	runGit(t, rig.tree, "init", "-q", "-b", "main", promisor)
	require.NoError(t, os.WriteFile(filepath.Join(promisor, "only"), []byte(t.Name()), 0o600))
	runGit(t, promisor, "add", "only")
	runGit(t, promisor, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "only the promisor holds this")
	commit := gitOut(t, promisor, "rev-parse", "HEAD")
	tree := gitOut(t, promisor, "rev-parse", "HEAD^{tree}")

	head := gitWithInput(t, rig.tree, "tree "+tree+"\nauthor t <t@example.com> 0 +0000\ncommitter t <t@example.com> 0 +0000\n\nthe tree is missing\n",
		"hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	runGit(t, rig.tree, "update-ref", "HEAD", head)

	marker, ran := newMarker(t)
	rig.plant(t, "core.repositoryformatversion", "1")
	rig.plant(t, "extensions.partialClone", "origin")
	rig.plant(t, "remote.origin.promisor", "true")
	kind(t, rig, promisor, marker)

	return promisorRig{pushRig: rig, commit: commit, missing: tree, ran: ran}
}

// promisorRead is an engine read in the worktree of an item that needs an object only the promisor holds.
type promisorRead struct {
	read func(ctx context.Context, rig promisorRig) error
	// control is the same read with plain git. wanted is the object that the read needs.
	control func(rig promisorRig) []string
	wanted  func(rig promisorRig) string
}

var promisorReads = map[string]promisorRead{
	"the read of a commit by the batch host": {
		read: func(ctx context.Context, rig promisorRig) error {
			_, err := batchHost{programs: programs{local: localRunner(repositoryProject(), gitexec.NewRepositories(rig.pulls.trees.repos))}}.tree(ctx, "p", rig.tree, rig.commit)

			return err
		},
		control: func(rig promisorRig) []string { return []string{"rev-parse", "--verify", rig.commit + "^{tree}"} },
		wanted:  func(rig promisorRig) string { return rig.commit },
	},
	"the read of the worktree by the gate": {
		read: func(ctx context.Context, rig promisorRig) error {
			_, err := projectGate{programs: programs{local: localRunner(repositoryProject(), gitexec.NewRepositories(rig.pulls.trees.repos))}}.tree(ctx, "p", rig.tree)

			return err
		},
		control: func(promisorRig) []string { return []string{"status", "--porcelain"} },
		wanted:  func(rig promisorRig) string { return rig.missing },
	},
	"the read of the status by the worktrees": {
		read: func(ctx context.Context, rig promisorRig) error {
			return gitExit("status", rig.tree, rig.pulls.trees.git(rig.tree).Run(ctx, []string{"status", "--porcelain"}))
		},
		control: func(promisorRig) []string { return []string{"status", "--porcelain"} },
		wanted:  func(rig promisorRig) string { return rig.missing },
	},
}

// A promisor remote in the clone config and an object that the clone lacks make a plain git read fetch the object from the promisor. Every engine read
// in the worktree is local: it fetches nothing, runs no ssh command of the clone config, and fails on the missing object. The control is the same read
// with plain git, which runs the ssh command or takes the object from the promisor.
func TestTheEngineReadsInTheWorktreeFetchNoMissingObjectFromAPromisor(t *testing.T) {
	t.Parallel()

	for read, path := range promisorReads {
		for kind, plant := range promisorKinds {
			t.Run(read+"/"+kind, func(t *testing.T) {
				t.Parallel()

				rig := newPromisorRig(t, plant)

				err := path.read(t.Context(), rig)

				require.DirExists(t, rig.tree)
				require.ErrorContains(t, err, rig.tree, "git runs in the worktree and fails on the object that the clone lacks")
				assert.NoFileExists(t, rig.ran, "the engine read runs no ssh command of the clone config")
				assert.False(t, holds(t, rig.tree, path.wanted(rig)), "the engine read took no object from the promisor")

				out := plainGit(t, rig.tree, path.control(rig)...)

				if kind == sshPromisor {
					assert.FileExists(t, rig.ran, "control: the plain read runs the ssh command of the clone config: %s", out)
				} else {
					assert.True(t, holds(t, rig.tree, path.wanted(rig)), "control: the plain read took the object from the promisor: %s", out)
				}
			})
		}
	}
}

// holds reports whether the repository of dir has the object, without a fetch from a promisor.
func holds(t *testing.T, dir, object string) bool {
	t.Helper()

	command := proc.GroupCommand(t.Context(), "git", []string{"cat-file", "-e", object}, dir)
	command.Env = append(command.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=file")

	return command.Run() == nil
}
