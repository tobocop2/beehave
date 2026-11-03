package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/sandbox/landlock"
)

// TestGateSandbox_TheLandlockEnforcerLetsAGateMakeAnyNewEntryAndNeverReachesTheRealHooks runs a gate under the real enforcer on a real repository. The gate
// makes and removes a new top-level file and folder, and every write that would land in a place where a hook runs later is refused: the hooks folder of the
// git folder, the folder that core.hooksPath names, and the hook folders of the real work folder.
func TestGateSandbox_TheLandlockEnforcerLetsAGateMakeAnyNewEntryAndNeverReachesTheRealHooks(t *testing.T) {
	t.Parallel()

	if err := landlock.New().Check(); err != nil {
		t.Skipf("Landlock cannot run on this machine: %v", err)
	}

	dir := repoWithCommit(t)
	hooksTarget := t.TempDir()
	runGit(t, dir, "config", "core.hooksPath", hooksTarget)

	box := sandboxFor(t, landlock.New(), nil)
	env := []string{"PATH=/usr/bin:/bin"}

	out, code := box.run(t.Context(), "p", dir, []string{"sh", "-c", "echo x > coverage.out && mkdir bin node_modules && echo y > bin/tool && rm -r bin node_modules coverage.out"}, env, 0)
	require.Zero(t, code, out)

	for name, hook := range map[string]string{
		"the hooks folder of the git folder": filepath.Join(dir, ".git", "hooks", "pre-commit"),
		"the folder core.hooksPath names":    filepath.Join(hooksTarget, "pre-commit"),
		"a hook folder of the real work":     filepath.Join(dir, ".husky", "pre-commit"),
		"the settings of the real work":      filepath.Join(dir, ".claude", "settings.json"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o700))

		out, code = box.run(t.Context(), "p", dir, []string{"sh", "-c", "echo 'touch /tmp/planted' > " + hook}, env, 0)
		assert.NotZero(t, code, "%s: %s", name, out)
		assert.NoFileExists(t, hook, name)
	}

	assert.Equal(t, []string{dir}, worktreesOf(t, dir), "nothing of the gate stays in the repository")
}
