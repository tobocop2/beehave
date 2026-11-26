package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// fetchRig is a push rig whose remote has a commit on main that the clone has not fetched, and a decoy remote with another commit on main.
type fetchRig struct {
	pushRig

	tip   string
	decoy string
	// control is the repository where the control fetch runs: the worktree of the rig by default.
	control string
}

func newFetchRig(t *testing.T) fetchRig {
	t.Helper()

	rig := newPushRig(t)
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, rig.tree, "clone", "-q", rig.remote, work)
	tip := commitIn(t, work, "new on the remote")
	runGit(t, work, "push", "-q", rig.remote, "HEAD:refs/heads/main")

	decoyWork := repoWithCommit(t)
	commitIn(t, decoyWork, "decoy")
	decoy := filepath.Join(t.TempDir(), "decoy.git")
	runGit(t, decoyWork, "clone", "-q", "--bare", ".", decoy)

	return fetchRig{pushRig: rig, tip: tip, decoy: decoy, control: rig.tree}
}

// fetchHostile is one setting that an agent could write into the git config of the shared clone, to steer a fetch from the remote.
type fetchHostile struct {
	// plant writes the setting into the config of the clone. marker is a script that records that it ran.
	plant func(t *testing.T, rig fetchRig, marker string)
	// allowed says whether the key is on the allow list of the clone config. The engine refuses a clone that holds any other key and runs no git there.
	allowed bool
	// steers says whether a fetch takes its objects from the decoy rather than running the marker.
	steers bool
}

var fetchHostiles = map[string]fetchHostile{
	"a hooks folder with a reference-transaction hook": {
		plant: func(t *testing.T, rig fetchRig, marker string) {
			t.Helper()

			hooks := t.TempDir()
			require.NoError(t, os.Symlink(marker, filepath.Join(hooks, "reference-transaction")))
			rig.plant(t, "core.hooksPath", hooks)
		},
	},
	"an ssh command behind a url rewrite": {
		plant: func(t *testing.T, rig fetchRig, marker string) {
			t.Helper()

			rig.plant(t, "url.ssh://example.invalid/remote.git.insteadOf", rig.remote)
			rig.plant(t, "core.sshCommand", marker)
		},
	},
	"an upload-pack program for the remote": {
		plant: func(t *testing.T, rig fetchRig, marker string) {
			t.Helper()

			rig.plant(t, "remote.origin.uploadpack", marker)
		},
	},
	"a credential helper behind a url rewrite": {
		plant: func(t *testing.T, rig fetchRig, marker string) {
			t.Helper()

			rig.plant(t, "url."+askingServer(t)+".insteadOf", rig.remote)
			rig.plant(t, "credential.helper", "!"+marker)
		},
	},
	"a url rewrite to another remote": {
		plant: func(t *testing.T, rig fetchRig, _ string) {
			t.Helper()

			rig.plant(t, "url."+rig.decoy+".insteadOf", rig.remote)
		},
		steers: true,
	},
	"another address for the remote": {
		plant: func(t *testing.T, rig fetchRig, _ string) {
			t.Helper()

			rig.plant(t, "remote.origin.url", rig.decoy)
		},
		steers:  true,
		allowed: true,
	},
	"an include file that rewrites the address": {
		plant: func(t *testing.T, rig fetchRig, _ string) {
			t.Helper()

			included := filepath.Join(filepath.Dir(rig.cloneConfig(t)), "included")
			require.NoError(t, os.WriteFile(included, []byte("[url \""+rig.decoy+"\"]\n\tinsteadOf = "+rig.remote+"\n"), 0o600))
			rig.plant(t, "include.path", included)
		},
		steers: true,
	},
}

// fetchCalls are the engine calls that fetch from the remote into the clone with the credential of the owner. Each returns the worktree it made.
// The refresh of a new item copies into the clone of the project, so the setting goes into that config. The refresh of a round item that has a worktree
// copies into the lane of the item, so the setting goes into the config of that lane, which the worktree shares.
var fetchCalls = map[string]struct {
	// prepare makes the worktree that the setting goes with, and points the rig at it. It is nil for the call that goes by the clone of the project.
	prepare func(t *testing.T, rig *fetchRig)
	run     func(t *testing.T, rig fetchRig) (string, error)
}{
	"the refresh of an item": {
		prepare: func(t *testing.T, rig *fetchRig) {
			t.Helper()

			clone := filepath.Join(rig.pulls.trees.repos, "p")
			rig.config = filepath.Join(clone, ".git", "config")
			rig.control = clone
		},
		run: func(t *testing.T, rig fetchRig) (string, error) {
			t.Helper()

			return rig.pulls.trees.Ensure(t.Context(), "p", "other", "", "")
		},
	},
	"the refresh of a round item": {
		prepare: func(t *testing.T, rig *fetchRig) {
			t.Helper()

			tree, err := rig.pulls.trees.Ensure(t.Context(), "p", "round/main", "", "")
			require.NoError(t, err)

			work := filepath.Join(t.TempDir(), "work")
			runGit(t, tree, "clone", "-q", rig.remote, work)
			rig.tip = commitIn(t, work, "later on the remote")
			runGit(t, work, "push", "-q", rig.remote, "HEAD:refs/heads/main")
			rig.tree, rig.control = tree, tree
		},
		run: func(t *testing.T, rig fetchRig) (string, error) {
			t.Helper()

			return rig.pulls.trees.Ensure(t.Context(), "p", "round/main", "", "")
		},
	},
}

// Each setting goes into a config that an engine fetch reads: the clone of the project, or the lane of a round item that has a worktree. Each engine fetch
// from the remote reads the remote that the engine read, and the setting does not take effect. The control is a plain git fetch in that repository,
// in which the same setting takes effect.
func TestWorktrees_FetchRunsNoSettingThatTheCloneConfigHolds(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	for call, fetch := range fetchCalls {
		for name, hostile := range fetchHostiles {
			t.Run(call+"/"+name, func(t *testing.T) {
				t.Parallel()

				rig := newFetchRig(t)
				ran := filepath.Join(t.TempDir(), "ran")
				marker := filepath.Join(t.TempDir(), "marker")
				require.NoError(t, exectest.Write(marker, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))
				decoyTip := gitOut(t, rig.decoy, "rev-parse", "refs/heads/main")

				fetch.prepare(t, &rig)

				hostile.plant(t, rig, marker)

				tree, err := fetch.run(t, rig)
				assert.NoFileExists(t, ran, "the setting in the clone config runs no program in the engine fetch")

				if hostile.allowed {
					require.NoError(t, err, "the engine fetch reads the remote that the engine read")
					assert.Equal(t, rig.tip, gitOut(t, tree, "rev-parse", "HEAD"), "the worktree starts at the tip of the remote that the engine read")
				} else {
					require.ErrorContains(t, err, "which beehave does not allow", "the engine refuses a clone whose config holds a key outside the allow list")
				}

				control := exec.CommandContext(t.Context(), "git", "fetch", "-q", "origin", "main:refs/control/main")
				control.Dir = rig.control
				control.Env = []string{"PATH=" + filepath.Dir(gitPath) + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_TERMINAL_PROMPT=0"}
				out, _ := control.CombinedOutput() //nolint:errcheck // the control shows the effect of the setting, whether or not git then fails

				if hostile.steers {
					assert.Equal(t, decoyTip, gitOut(t, rig.control, "rev-parse", "FETCH_HEAD"), "control: a plain fetch in the repository reads the decoy: %s", out)
				} else {
					assert.FileExists(t, ran, "control: a plain fetch in the repository runs the setting: %s", out)
				}
			})
		}
	}
}

// hooksHostile names the setting that a read of the branches cannot take: git ls-remote writes no ref, so no hook runs.
const hooksHostile = "a hooks folder with a reference-transaction hook"

// Each setting goes into the config of the clone. The release reads the branches of the remote that the engine read, and the setting does not take
// effect. The control is the read that the release made in the worktree before, git ls-remote --heads origin, in which the same setting takes effect.
func TestWorktrees_HeadsReadsNoSettingThatTheCloneConfigHolds(t *testing.T) {
	t.Parallel()

	for name, hostile := range fetchHostiles {
		if name == hooksHostile {
			continue
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newFetchRig(t)
			marker, ran := newMarker(t)
			decoyTip := gitOut(t, rig.decoy, "rev-parse", "refs/heads/main")

			hostile.plant(t, rig, marker)

			heads, err := rig.pulls.trees.Heads(t.Context(), "p")
			require.NoError(t, err, "the release reads the remote that the engine read")
			assert.Contains(t, heads, rig.tip+"\trefs/heads/main", "the release lists the tip of the remote that the engine read")
			assert.NoFileExists(t, ran, "the setting in the clone config runs no program in the read of the release")

			out := plainGit(t, rig.tree, "ls-remote", "--heads", "origin")

			if hostile.steers {
				assert.Contains(t, out, decoyTip, "control: a plain read in the worktree lists the decoy")
			} else {
				assert.FileExists(t, ran, "control: a plain read in the worktree runs the setting: %s", out)
			}
		})
	}
}

// Each setting goes into the config of the clone. The tip of a batch fetches the base from the remote that the engine read, and the setting does not
// take effect. The control is a plain git fetch in the worktree, in which the same setting takes effect.
func TestBatchHost_TipFetchesNoSettingThatTheCloneConfigHolds(t *testing.T) {
	t.Parallel()

	for name, hostile := range fetchHostiles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newFetchRig(t)
			clone := filepath.Join(rig.pulls.trees.repos, "p")
			rig.config = filepath.Join(clone, ".git", "config")
			marker, ran := newMarker(t)
			decoyTip := gitOut(t, rig.decoy, "rev-parse", "refs/heads/main")

			hostile.plant(t, rig, marker)

			host := batchHost{
				pulls:    pulls{projects: rig.pulls.projects, forge: func(model.Project) (forge.Forge, error) { return hostForge{tip: rig.tip, found: true}, nil }},
				programs: programs{trees: rig.pulls.trees, local: localRunner(repositoryProject(), gitexec.NewRepositories(rig.pulls.trees.repos))},
				base:     rig.pulls.trees,
			}

			tip, err := host.Tip(t.Context(), "p", pushItem, "main")
			assert.NoFileExists(t, ran, "the setting in the clone config runs no program in the fetch of the tip")

			if hostile.allowed {
				require.NoError(t, err, "the tip fetches the remote that the engine read")
				assert.Equal(t, gitOut(t, rig.remote, "rev-parse", rig.tip+"^{tree}"), tip.Tree, "the clone holds the tip of the remote that the engine read")
			} else {
				require.ErrorContains(t, err, "which beehave does not allow", "the tip refuses a clone whose config holds a key outside the allow list")
			}

			out := plainGit(t, clone, "fetch", "-q", "origin", "main:refs/control/main")

			if hostile.steers {
				assert.Equal(t, decoyTip, gitOut(t, clone, "rev-parse", "FETCH_HEAD"), "control: a plain fetch in the clone reads the decoy: %s", out)
			} else {
				assert.FileExists(t, ran, "control: a plain fetch in the clone runs the setting: %s", out)
			}
		})
	}
}

// The tip of a batch fetches what git fetch origin fetched in the worktree: every branch of the remote, and the tags that point into them.
func TestBatchHost_TipFetchesEveryBranchAndTheTagsOfTheRemote(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, rig.tree, "clone", "-q", rig.remote, work)
	other := commitIn(t, work, "on another branch")
	runGit(t, work, "push", "-q", rig.remote, "HEAD:refs/heads/other")
	runGit(t, work, "tag", "v1", rig.tip)
	runGit(t, work, "push", "-q", rig.remote, "refs/tags/v1")

	host := batchHost{
		pulls:    pulls{projects: rig.pulls.projects, forge: func(model.Project) (forge.Forge, error) { return hostForge{tip: rig.tip, found: true}, nil }},
		programs: programs{trees: rig.pulls.trees, local: localRunner(repositoryProject(), gitexec.NewRepositories(rig.pulls.trees.repos))},
		base:     rig.pulls.trees,
	}

	_, err := host.Tip(t.Context(), "p", pushItem, "main")

	require.NoError(t, err)
	assert.Equal(t, rig.tip, gitOut(t, rig.tree, "rev-parse", "refs/remotes/origin/main"), "the clone holds the base of the remote")
	assert.Equal(t, other, gitOut(t, rig.tree, "rev-parse", "refs/remotes/origin/other"), "the clone holds every other branch of the remote")
	assert.Equal(t, rig.tip, gitOut(t, rig.tree, "rev-parse", "refs/tags/v1"), "the clone holds the tags of the remote")
}

// A rewrite in the clone config can turn the folder of the push repository into an ssh address. The copy into the clone takes the file transport only,
// so the ssh command never runs and the refresh fails. The control is a plain fetch of that folder in the worktree, which runs the ssh command.
func TestWorktrees_TheCopyIntoTheCloneTakesTheFileTransportOnly(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	clone := filepath.Join(rig.opts.StateDir, "repos", "p")
	rig.config = filepath.Join(clone, ".git", "config")
	marker, ran := newMarker(t)
	push := filepath.Join(rig.opts.StateDir, "push", "p.git")
	rig.plant(t, "url.ssh://example.invalid/push.git.insteadOf", push)
	rig.plant(t, "core.sshCommand", marker)

	_, err := rig.pulls.trees.Ensure(t.Context(), "p", "other", "", "")

	require.ErrorContains(t, err, "git fetch in "+filepath.Join(rig.opts.StateDir, "repos", "p"), "the copy into the clone refuses the ssh address")
	assert.NoFileExists(t, ran, "the ssh command of the clone config never runs")

	out := plainGit(t, clone, "fetch", "-q", push, "refs/heads/main")
	assert.FileExists(t, ran, "control: a plain fetch of the push repository in the clone runs the ssh command: %s", out)
}

// The clone has no config before the first clone, so the first clone runs with every transport of git. The control is the server, which sees the
// request that a clone limited to the file transport never sends.
func TestWorktrees_TheFirstCloneReachesTheRemoteOverItsOwnTransport(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: server.URL + "/remote.git", BaseBranch: "main"}}}
	trees := newWorktrees(projects, flow.Options{StateDir: t.TempDir()}, nil, nil)

	_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.ErrorContains(t, err, "git clone")
	assert.NotContains(t, err.Error(), "not allowed", "the first clone takes the transport of its address")
	assert.Positive(t, asked.Load(), "the first clone asks the remote")
}

// newMarker writes a program that makes the file ran when it runs, and returns both paths.
func newMarker(t *testing.T) (marker, ran string) {
	t.Helper()

	ran = filepath.Join(t.TempDir(), "ran")
	marker = filepath.Join(t.TempDir(), "marker")
	require.NoError(t, exectest.Write(marker, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))

	return marker, ran
}

// plainGit runs git in dir with an empty home and returns what it printed, whether or not it failed.
func plainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = dir
	command.Env = []string{"PATH=" + filepath.Dir(gitPath) + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_TERMINAL_PROMPT=0"}
	out, _ := command.CombinedOutput() //nolint:errcheck // the control shows the effect of the setting, whether or not git then fails

	return string(out)
}

func TestWorktrees_HeadsNamesWhyItCannotReadTheRemote(t *testing.T) {
	t.Parallel()

	t.Run("a project that the config lacks", func(t *testing.T) {
		t.Parallel()

		trees, _, _ := ensureFixture(t)

		_, err := trees.Heads(t.Context(), "nope")

		require.ErrorIs(t, err, errUnknownProject)
	})

	t.Run("a project whose forge cannot be built", func(t *testing.T) {
		t.Parallel()

		trees, git, _ := ensureFixture(t)
		trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

		_, err := trees.Heads(t.Context(), "p")

		require.ErrorIs(t, err, errFake)
		assert.Empty(t, git.calls, "no git call runs with no address of the remote")
	})

	t.Run("a push repository that cannot be made", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		trees.remote = (&fakeGit{fail: "init"}).runner

		_, err := trees.Heads(t.Context(), "p")

		require.ErrorContains(t, err, "git init in "+filepath.Join(state, "push", "p.git"))
	})

	t.Run("a remote that git cannot read", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		git := &fakeGit{fail: "ls-remote"}
		trees.remote = git.runner

		_, err := trees.Heads(t.Context(), "p")

		require.ErrorContains(t, err, "git ls-remote in "+filepath.Join(state, "push", "p.git"))
		assert.Contains(t, git.calls, filepath.Join(state, "push", "p.git")+": --git-dir="+filepath.Join(state, "push", "p.git")+" ls-remote --heads https://example.test/owner/name.git",
			"the read names the address of the forge, never a remote name")
	})
}
