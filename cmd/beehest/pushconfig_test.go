package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
)

// hostileConfig is one setting that an agent could write into the git config of the shared clone, and the way to see that it took effect.
type hostileConfig struct {
	// plant writes the setting into the config of the clone. marker is a script that records that it ran, and decoy is a second bare remote.
	plant func(t *testing.T, rig pushRig, marker, decoy string)
	// took says whether the setting took effect: the marker ran, or the branch of the item landed on the decoy.
	took func(t *testing.T, ran, decoy string) bool
}

// cloneConfig is the config file of the clone that the worktree of the rig shares.
func (r pushRig) cloneConfig(t *testing.T) string {
	t.Helper()

	return filepath.Join(gitOut(t, r.tree, "rev-parse", "--path-format=absolute", "--git-common-dir"), "config")
}

// plant sets key to value in the config of the clone.
func (r pushRig) plant(t *testing.T, key, value string) {
	t.Helper()

	runGit(t, r.tree, "config", "--file", r.cloneConfig(t), key, value)
}

func markerRan(_ *testing.T, ran, _ string) bool {
	_, err := os.Stat(ran)

	return err == nil
}

func decoyHasTheBranch(t *testing.T, _, decoy string) bool {
	t.Helper()

	return gitOut(t, decoy, "branch", "--list", pushItem) != ""
}

// askingServer is a git server over HTTP that asks for a password on every request, so git calls its credential helper.
func askingServer(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	return server.URL + "/remote.git"
}

var hostileConfigs = map[string]hostileConfig{
	"a hooks folder with a pre-push hook": {
		plant: func(t *testing.T, rig pushRig, marker, _ string) {
			t.Helper()

			hooks := t.TempDir()
			require.NoError(t, os.Symlink(marker, filepath.Join(hooks, "pre-push")))
			rig.plant(t, "core.hooksPath", hooks)
		},
		took: markerRan,
	},
	"an ssh command behind a url rewrite": {
		plant: func(t *testing.T, rig pushRig, marker, _ string) {
			t.Helper()

			rig.plant(t, "url.ssh://example.invalid/remote.git.insteadOf", rig.remote)
			rig.plant(t, "core.sshCommand", marker)
		},
		took: markerRan,
	},
	"a receive-pack program for the remote": {
		plant: func(t *testing.T, rig pushRig, marker, _ string) {
			t.Helper()

			rig.plant(t, "remote.origin.receivepack", marker)
		},
		took: markerRan,
	},
	"a credential helper behind a url rewrite": {
		plant: func(t *testing.T, rig pushRig, marker, _ string) {
			t.Helper()

			rig.plant(t, "url."+askingServer(t)+".insteadOf", rig.remote)
			rig.plant(t, "credential.helper", "!"+marker)
		},
		took: markerRan,
	},
	"a url rewrite to another remote": {
		plant: func(t *testing.T, rig pushRig, _, decoy string) {
			t.Helper()

			rig.plant(t, "url."+decoy+".insteadOf", rig.remote)
		},
		took: decoyHasTheBranch,
	},
	"an include file that sets another push address": {
		plant: func(t *testing.T, rig pushRig, _, decoy string) {
			t.Helper()

			// The file sits in the git folder of the clone, where an agent can write and the copy of the head can read.
			included := filepath.Join(filepath.Dir(rig.cloneConfig(t)), "included")
			require.NoError(t, os.WriteFile(included, []byte("[remote \"origin\"]\n\tpushurl = "+decoy+"\n"), 0o600))
			rig.plant(t, "include.path", included)
		},
		took: decoyHasTheBranch,
	},
}

// Each setting goes into the config of the clone, where an agent whose worktree shares the clone could write it. The engine push lands on the remote that
// the engine read, and the setting does not take effect. The control is a plain git push from the worktree, in which the same setting takes effect.
func TestPulls_PushRunsNoSettingThatTheCloneConfigHolds(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	for name, hostile := range hostileConfigs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newPushRig(t)
			ran := filepath.Join(t.TempDir(), "ran")
			marker := filepath.Join(t.TempDir(), "marker")
			require.NoError(t, exectest.Write(marker, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))
			decoy := filepath.Join(t.TempDir(), "decoy.git")
			runGit(t, rig.tree, "init", "-q", "--bare", decoy)

			hostile.plant(t, rig, marker, decoy)

			require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))
			assert.Equal(t, rig.head, rig.remoteHead(t), "the push lands on the remote that the engine read")
			assert.False(t, hostile.took(t, ran, decoy), "the setting in the clone config took no effect on the engine push")

			control := exec.CommandContext(t.Context(), "git", "push", "-q", "origin", "HEAD:refs/heads/"+pushItem)
			control.Dir = rig.tree
			control.Env = []string{"PATH=" + filepath.Dir(gitPath) + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_TERMINAL_PROMPT=0"}
			out, _ := control.CombinedOutput() //nolint:errcheck // the control shows the effect of the setting, whether or not git then fails
			assert.True(t, hostile.took(t, ran, decoy), "control: a plain push from the worktree uses the setting: %s", out)
		})
	}
}
