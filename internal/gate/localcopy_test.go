package gate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localWriteThroughTheClone is head code that writes every place that a gate copy could share with the item or the source: the loose objects of the clone that are hard
// links, the item worktree, and the hooks and info folders of the git folder that the clone came from. It finds the last three by the address of the clone.
const localWriteThroughTheClone = `for f in $(find .git/objects -type f -links +1); do chmod u+w "$f"; printf x >> "$f"; done
o=$(git config --get remote.origin.url)
[ -n "$o" ] && { printf x > "$o/pwned-wt.txt"; c=$(git -C "$o" rev-parse --git-common-dir); case $c in /*) ;; *) c=$o/$c;; esac
printf x > "$c/hooks/pwned-hook"; printf x > "$c/info/pwned-info"; }
exit 0
`

// folderDigest is the digest of the bytes, the mode and the link target of every entry below dir.
func folderDigest(t *testing.T, dir string) map[string]string {
	t.Helper()

	digest := map[string]string{}

	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			digest[path] = "link " + target

			return err
		case entry.IsDir():
			digest[path] = "dir"
		default:
			data, err := os.ReadFile(path) //nolint:gosec // the test reads the folders that it made
			digest[path] = fmt.Sprintf("%v %x", info.Mode(), sha256.Sum256(data))

			return err
		}

		return nil
	}))

	return digest
}

func TestRun_ALocalGateWithAGateProgramLeavesTheItemAndTheSourceUnchangedWhenHeadCodeWritesThem(t *testing.T) {
	t.Parallel()

	item, request := localItem(t, programCase{files: map[string]string{"code.sh": localWriteThroughTheClone}})
	source := request.CloneURL

	// The item folder and its git folder are the same repository as the source here, so the digest of the source holds the objects, the hooks and the info folder.
	shIn(t, item, "git status --porcelain")

	itemBefore, sourceBefore := folderDigest(t, item), folderDigest(t, source)

	result, err := localProgramGate(t, item).Run(context.Background(), request)
	require.NoError(t, err)
	require.True(t, result.Passed, result.Report)

	assert.Equal(t, itemBefore, folderDigest(t, item))
	assert.Equal(t, sourceBefore, folderDigest(t, source))
}

func TestRun_ALocalGateWithAGateProgramCopiesTheObjectsOfTheItemAndKeepsNoAddressOfIt(t *testing.T) {
	t.Parallel()

	checks := "[ -z \"$(git config --get remote.origin.url)\" ] || printf 'BAD origin kept\\n' > code.txt\n" +
		"[ -z \"$(find .git/objects -type f -links +1)\" ] || printf 'BAD objects shared\\n' > code.txt\n"
	item, request := localItem(t, programCase{files: map[string]string{"code.sh": checks}})

	result, err := localProgramGate(t, item).Run(context.Background(), request)
	require.NoError(t, err)

	assert.True(t, result.Passed, result.Report)
}

// localPlantedTools writes the tools that an agent step could put first on the PATH before the gate: a git that hides the changed paths, and a digest tool that
// gives the digest of the base gate.cfg for gate.cfg. It returns the folder and the PATH that holds it before the clean one.
func localPlantedTools(t *testing.T, tools Tools) (folder, path string) {
	t.Helper()

	folder = t.TempDir()
	hide := "#!/bin/sh\nfor a; do [ \"$a\" = diff-tree ] && exit 0; done\nexec " + tools.Git + " \"$@\"\n"
	digest := fmt.Sprintf("#!/bin/sh\nfor a; do f=$a; done\n[ \"$f\" = gate.cfg ] && { echo \"%x  gate.cfg\"; exit 0; }\nexec %s \"$@\"\n", sha256.Sum256([]byte("strict=1\n")), tools.Digest)

	for name, text := range map[string]string{"git": hide, "sha256sum": digest, "shasum": digest} {
		require.NoError(t, os.WriteFile(filepath.Join(folder, name), []byte(text), 0o700)) //nolint:gosec // a tool that the test plants
	}

	return folder, folder + ":" + localCleanPath
}

func TestRun_ALocalGateWithAGateProgramUsesTheToolsThatTheEngineResolvedAtItsStart(t *testing.T) {
	t.Parallel()

	tools := localTools(t)

	cases := []struct {
		name   string
		files  map[string]string
		reason string
	}{
		{name: "a_git_that_hides_the_changed_paths", files: map[string]string{"Makefile": programWeakMakefile}, reason: "the head changed the gate program, paths Makefile"},
		{name: "a_digest_tool_that_repeats_the_base_digest", files: map[string]string{"code.sh": "printf 'strict=0\\n' > gate.cfg\n", "code.txt": "BAD\n"}, reason: "the gate program changed while the gate ran: gate.cfg"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			item, request := localItem(t, programCase{files: c.files})
			_, planted := localPlantedTools(t, tools)

			result, err := localProgramGateOn(t, item, planted, tools).Run(context.Background(), request)
			require.NoError(t, err)

			assert.False(t, result.Passed, result.Report)
			assert.Contains(t, result.Report, c.reason)

			// The control: a job that asks the PATH of the job for its tools takes the planted ones, so the plant is live.
			item, request = localItem(t, programCase{files: c.files})
			result, err = localProgramGateOn(t, item, planted, Tools{}).Run(context.Background(), request)
			require.NoError(t, err)

			assert.True(t, result.Passed, "the planted tool does not defeat a job that finds its tools on the PATH: "+result.Report)
		})
	}
}
