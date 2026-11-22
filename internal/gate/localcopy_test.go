package gate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
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
		require.NoError(t, exectest.Write(filepath.Join(folder, name), text))
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

// localHookCount is the number of times that the planted hooks ran: one line in the file count for each run.
func localHookCount(t *testing.T, count string) int {
	t.Helper()

	data, err := os.ReadFile(count) //nolint:gosec // the test reads the file that its hook writes
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	return strings.Count(string(data), "\n")
}

func TestRun_ALocalGateWithAGateProgramRunsNoHookThatAGitConfigOfTheUserOrTheCallerNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  func(dir, hooks string) []string
	}{
		{name: "the_global_file", env: func(dir, hooks string) []string {
			writeIn(t, dir, "global", "[core]\n\thooksPath = "+hooks+"\n")

			return []string{"GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "global"), "GIT_CONFIG_NOSYSTEM=1"}
		}},
		{name: "the_file_under_xdg_config_home", env: func(dir, hooks string) []string {
			writeIn(t, dir, "xdg/git/config", "[core]\n\thooksPath = "+hooks+"\n")

			return []string{"XDG_CONFIG_HOME=" + filepath.Join(dir, "xdg"), "GIT_CONFIG_NOSYSTEM=1"}
		}},
		{name: "the_system_file", env: func(dir, hooks string) []string {
			writeIn(t, dir, "system", "[core]\n\thooksPath = "+hooks+"\n")

			return []string{"GIT_CONFIG_SYSTEM=" + filepath.Join(dir, "system"), "GIT_CONFIG_GLOBAL=/dev/null"}
		}},
		{name: "the_config_values_of_the_caller", env: func(_, hooks string) []string {
			return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=" + hooks, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		}},
		{name: "the_config_parameters_of_the_caller", env: func(_, hooks string) []string {
			return []string{"GIT_CONFIG_PARAMETERS='core.hookspath'='" + hooks + "'", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			hooks, count := filepath.Join(dir, "hooks"), filepath.Join(dir, "count")
			require.NoError(t, os.Mkdir(hooks, 0o750))

			for _, name := range []string{"post-checkout", "reference-transaction"} {
				require.NoError(t, exectest.Write(filepath.Join(hooks, name), "#!/bin/sh\necho "+name+" >> "+count+"\nexit 0\n"))
			}

			env := append([]string{"PATH=" + localCleanPath, "HOME=" + filepath.Join(dir, "home")}, c.env(dir, hooks)...)
			item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})

			// The control: git with this environment runs the planted hook, so the plant is live.
			control := exec.CommandContext(t.Context(), localTools(t).Git, "clone", "-q", "--", item, filepath.Join(dir, "control")) //nolint:gosec // the test git
			control.Env = env
			out, err := control.CombinedOutput()
			require.NoError(t, err, string(out))
			ran := localHookCount(t, count)
			require.Positive(t, ran, "the planted hook does not run for git with this environment")

			result, err := localProgramGateEnv(t, item, localTools(t), env).Run(context.Background(), request)
			require.NoError(t, err)

			assert.True(t, result.Passed, result.Report)
			assert.Equal(t, ran, localHookCount(t, count), "a git call of the gate job ran the hook that a git config of the user or the caller names")
		})
	}
}

func TestRun_ALocalGateWithAGateProgramChecksOutTheHeadWithNoAttributesFileOfTheUser(t *testing.T) {
	t.Parallel()

	// The head code fails the gate when the checkout wrote code.txt with a carriage return, as the attributes file of the user asks.
	crlf := "case \"$(cat code.txt)\" in *\"$(printf '\\r')\"*) printf 'BAD\\n' > code.txt ;; esac\n"

	cases := []struct {
		name string
		file string
		env  func(dir string) []string
	}{
		{name: "the_file_under_xdg_config_home", file: "xdg/git/attributes", env: func(dir string) []string {
			return []string{"XDG_CONFIG_HOME=" + filepath.Join(dir, "xdg")}
		}},
		{name: "the_file_under_the_config_folder_of_home", file: "home/.config/git/attributes", env: func(string) []string { return nil }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeIn(t, dir, c.file, "code.txt text eol=crlf\n")

			env := slices.Concat([]string{"PATH=" + localCleanPath, "HOME=" + filepath.Join(dir, "home"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, c.env(dir))
			item, request := localItem(t, programCase{files: map[string]string{"code.sh": crlf}})

			// The control: git with this environment writes code.txt with a carriage return, so the plant is live.
			control := exec.CommandContext(t.Context(), localTools(t).Git, "clone", "-q", "--", item, filepath.Join(dir, "control")) //nolint:gosec // the test git
			control.Env = env
			out, err := control.CombinedOutput()
			require.NoError(t, err, string(out))

			data, err := os.ReadFile(filepath.Join(dir, "control", "code.txt")) //nolint:gosec // the test reads the clone that it made
			require.NoError(t, err)
			require.Contains(t, string(data), "\r", "the planted attributes file does not change the checkout of git with this environment")

			result, err := localProgramGateEnv(t, item, localTools(t), env).Run(context.Background(), request)
			require.NoError(t, err)

			assert.True(t, result.Passed, "a git call of the gate job read the attributes file of the user: "+result.Report)
		})
	}
}
