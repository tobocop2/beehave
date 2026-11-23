package gomod_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/gomod"
)

// rig is a filler over a fake go command, an item worktree and a cache root, all in temporary folders.
type rig struct {
	filler gomod.Filler
	work   string
	calls  string
}

// newRig returns a rig whose fake go runs the real go for edit commands, and runs download for go mod download.
// The fake writes each call as an args= line followed by the environment it got.
func newRig(t *testing.T, download string) rig {
	t.Helper()

	real, err := exec.LookPath("go")
	require.NoError(t, err)

	bin, work := t.TempDir(), evaluated(t, t.TempDir())
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\n{ echo \"args=$*\"; echo \"DIR=$(pwd -P)\"; env; } >> '" + calls + "'\n" +
		"case \"$1 $2\" in\n\"mod edit\"|\"work edit\") [ -f '" + bin + "/edit.out' ] && exec cat '" + bin + "/edit.out'; exec '" + real + "' \"$@\" ;;\nesac\n" + strings.ReplaceAll(download, "REALGO", real) + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "go"), script))

	return rig{filler: gomod.Filler{Root: filepath.Join(t.TempDir(), "gomodcache"), Path: bin + ":/usr/bin:/bin"}, work: work, calls: calls}
}

// evaluated returns the folder with its links resolved, as the filler compares paths.
func evaluated(t *testing.T, dir string) string {
	t.Helper()

	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return real
}

func write(t *testing.T, path, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}

// downloads returns the environment of each go mod download call the fake recorded, one map per call.
func (r rig) downloads(t *testing.T) []map[string]string {
	t.Helper()

	raw, err := os.ReadFile(r.calls)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	var calls []map[string]string

	for _, call := range strings.Split(string(raw), "args=")[1:] {
		lines := strings.Split(call, "\n")
		if lines[0] != "mod download all" {
			continue
		}

		env := map[string]string{}

		for _, line := range lines[1:] {
			if name, value, found := strings.Cut(line, "="); found {
				env[name] = value
			}
		}

		calls = append(calls, env)
	}

	return calls
}

const module = "module example.com/p\n\ngo 1.22\n"

const fillsCache = `mkdir -p "$GOMODCACHE/cache/download/example.com/dep/@v" && touch "$GOMODCACHE/cache/download/example.com/dep/@v/v1.0.0.zip" && echo "example.com/dep v1.0.0 h1:x=" >> go.sum`

func TestFill_ProjectWithDependenciesFillsTheCacheAndGoSum(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module+"\nrequire example.com/dep v1.0.0\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, note)
	assert.Equal(t, filepath.Join(r.filler.Root, "proj"), cache)
	assert.FileExists(t, filepath.Join(cache, "cache", "download", "example.com", "dep", "@v", "v1.0.0.zip"))
	sum, err := os.ReadFile(filepath.Join(r.work, "go.sum"))
	require.NoError(t, err)
	assert.Contains(t, string(sum), "example.com/dep v1.0.0 h1:")
	require.Len(t, r.downloads(t), 1)
	assert.NotEqual(t, r.work, r.downloads(t)[0]["DIR"], "go never runs in the files the agent wrote")
	assert.Equal(t, "tree", filepath.Base(r.downloads(t)[0]["DIR"]), "the download runs in the copy of the engine")
	assert.NoFileExists(t, filepath.Join(r.work, "go.sum.beehave"), "the new go.sum replaced the old one")
}

func TestFill_AProjectNameIsOnePathElementOfTheCache(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, _ := r.filler.Fill(t.Context(), "../a/b", r.work)

	assert.Equal(t, filepath.Join(r.filler.Root, "..%2Fa%2Fb"), cache)
}

func TestFill_NoGoModStartsNothing(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "main.go"), "package main\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, cache)
	assert.Empty(t, note)
	assert.NoFileExists(t, r.calls, "no go command ran")
	assert.NoDirExists(t, r.filler.Root)
}

func TestFill_VendorFolderStartsNothing(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)
	write(t, filepath.Join(r.work, "vendor", "modules.txt"), "# example.com/dep v1.0.0\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, cache)
	assert.Empty(t, note)
	assert.NoFileExists(t, r.calls, "no go command ran")
}

func TestFill_WorkspaceInsideTheWorktreeFillsTheCache(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse (\n\t./a\n\t./b\n)\n")
	write(t, filepath.Join(r.work, "a", "go.mod"), "module example.com/a\n\ngo 1.22\n\nreplace example.com/b => ../b\n")
	write(t, filepath.Join(r.work, "b", "go.mod"), "module example.com/b\n\ngo 1.22\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, note)
	assert.NotEmpty(t, cache)
	require.Len(t, r.downloads(t), 1)
	work := r.downloads(t)[0]["GOWORK"]
	assert.True(t, strings.HasPrefix(work, r.filler.Root), "GOWORK names the go.work of the copy, not of the worktree: %s", work)
	assert.Equal(t, "go.work", filepath.Base(work))
}

func TestFill_WorkspaceOutsideTheWorktreeIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	outside := filepath.Join(filepath.Dir(r.work), "outside")
	write(t, filepath.Join(outside, "go.mod"), "module example.com/o\n\ngo 1.22\n")
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse ../outside\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, outside)
	assert.Contains(t, note, "outside the worktree")
	assert.Equal(t, filepath.Join(r.filler.Root, "proj"), cache, "the step starts with the cache as it is")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AWorkspaceReplaceOutsideTheWorktreeIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	outside := filepath.Join(t.TempDir(), "x")
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse ./a\n\nreplace example.com/x => "+outside+"\n")
	write(t, filepath.Join(r.work, "a", "go.mod"), "module example.com/a\n\ngo 1.22\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, outside)
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_LocalReplaceOutsideTheWorktreeIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	outside := filepath.Join(t.TempDir(), "dep")
	write(t, filepath.Join(r.work, "go.mod"), module+"\nreplace example.com/dep v1.0.0 => example.com/fork v1.0.1\n\nreplace example.com/x => "+outside+"\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, outside)
	assert.Contains(t, note, filepath.Join(r.work, "go.mod"))
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AFolderBesideTheWorktreeWithTheSameNameStartIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	beside := r.work + "-other"
	write(t, filepath.Join(r.work, "go.mod"), module+"\nreplace example.com/x => "+beside+"\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, beside)
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AGoSumLinkToANewOutsideFileIsRefusedAndNothingIsWrittenOutside(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	victim := filepath.Join(t.TempDir(), "victim")
	write(t, filepath.Join(r.work, "go.mod"), module+"\nrequire example.com/dep v1.0.0\n")
	require.NoError(t, os.Symlink(victim, filepath.Join(r.work, "go.sum")))

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.NoFileExists(t, victim, "the engine created no file outside the worktree")
	assert.Contains(t, note, "go.sum")
	assert.Contains(t, note, "link")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AGoSumLinkToAnExistingOutsideFileLeavesItAsItIs(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	victim := filepath.Join(t.TempDir(), "victim")
	write(t, victim, "keep\n")
	write(t, filepath.Join(r.work, "go.mod"), module+"\nrequire example.com/dep v1.0.0\n")
	require.NoError(t, os.Symlink(victim, filepath.Join(r.work, "go.sum")))

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	kept, err := os.ReadFile(victim) //nolint:gosec // a path inside the test folder
	require.NoError(t, err)
	assert.Equal(t, "keep\n", string(kept), "the engine changed no file outside the worktree")
	assert.Contains(t, note, "link")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AGoWorkSumLinkToAnOutsideFileIsRefusedAndNothingIsWrittenOutside(t *testing.T) {
	t.Parallel()

	r := newRig(t, `echo "example.com/dep v1.0.0 h1:x=" >> go.work.sum`)
	victim := filepath.Join(t.TempDir(), "victim")
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse ./a\n")
	write(t, filepath.Join(r.work, "a", "go.mod"), "module example.com/a\n\ngo 1.22\n")
	require.NoError(t, os.Symlink(victim, filepath.Join(r.work, "go.work.sum")))

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.NoFileExists(t, victim, "the engine created no file outside the worktree")
	assert.Contains(t, note, "go.work.sum")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_ALinkInTheWorktreeToAFolderOutsideIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	outside := evaluated(t, t.TempDir())
	write(t, filepath.Join(outside, "go.mod"), "module example.com/o\n\ngo 1.22\n")
	require.NoError(t, os.Symlink(outside, filepath.Join(r.work, "o")))
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse ./o\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, filepath.Join(r.work, "o")+" is a link")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AGoSumThatIsAFolderIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)
	require.NoError(t, os.MkdirAll(filepath.Join(r.work, "go.sum"), 0o700))

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, filepath.Join(r.work, "go.sum")+" is a link or not a regular file")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AGoSumTheDownloadLeavesAsItIsIsNotRewritten(t *testing.T) {
	t.Parallel()

	r := newRig(t, `true`)
	write(t, filepath.Join(r.work, "go.mod"), module)
	write(t, filepath.Join(r.work, "go.sum"), "example.com/dep v1.0.0 h1:x=\n")
	before, err := os.Stat(filepath.Join(r.work, "go.sum"))
	require.NoError(t, err)

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, note)
	after, err := os.Stat(filepath.Join(r.work, "go.sum"))
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the go.sum file is the same file")
}

func TestFill_AGoSumThatCannotBeWrittenBackIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)
	require.NoError(t, os.MkdirAll(filepath.Join(r.work, "go.sum.beehave"), 0o700))

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.NotEmpty(t, cache, "the step starts with the cache")
	assert.Contains(t, note, "cannot be written to "+filepath.Join(r.work, "go.sum"))
	assert.NoFileExists(t, filepath.Join(r.work, "go.sum"))
}

func TestFill_AGoSumThatCannotBeReadIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)
	write(t, filepath.Join(r.work, "go.sum"), "")
	require.NoError(t, os.Chmod(filepath.Join(r.work, "go.sum"), 0o000))

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, filepath.Join(r.work, "go.sum")+" cannot be copied")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AWorktreeThatCannotBeOpenedIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)
	require.NoError(t, exectest.ChmodFolder(r.work, 0o100), "a folder that can be searched but not read")
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(r.work, 0o700)) })

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "the worktree cannot be opened")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_RunsGoWithAFixedEnvironment(t *testing.T) {
	for _, name := range []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOFLAGS", "GOPROXY", "GOMODCACHE", "GOTOOLCHAIN", "GOENV"} {
		t.Setenv(name, "owner")
	}

	t.Setenv("NETRC", "/owner/.netrc")

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, _ := r.filler.Fill(t.Context(), "proj", r.work)

	require.Len(t, r.downloads(t), 1)
	env := r.downloads(t)[0]
	assert.Equal(t, r.filler.Path, env["PATH"])
	assert.Equal(t, "off", env["GOENV"])
	assert.Equal(t, "-mod=readonly", env["GOFLAGS"])
	assert.Equal(t, "https://proxy.golang.org", env["GOPROXY"], "no direct fallback, so go never runs git")
	assert.Equal(t, "local", env["GOTOOLCHAIN"])
	assert.Equal(t, cache, env["GOMODCACHE"])
	assert.Equal(t, "off", env["GOWORK"], "a go.work in a parent folder is not used")
	assert.NotContains(t, env, "GOSUMDB", "the checksum database stays at its default")

	for _, name := range []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "NETRC"} {
		assert.NotContains(t, env, name)
	}

	home := env["HOME"]
	require.NotEmpty(t, home)
	assert.NotEqual(t, filepath.Dir(r.work), home)
	assert.NoDirExists(t, home, "the empty home of the download is removed after it")
	assert.True(t, strings.HasPrefix(env["GOCACHE"], home), "the build cache of the download is in its home")
}

func TestFill_AGoVersionNewerThanTheLocalGoIsANoteAndNoToolchainDownload(t *testing.T) {
	t.Parallel()

	r := newRig(t, `exec 'REALGO' "$@"`)
	write(t, filepath.Join(r.work, "go.mod"), "module example.com/p\n\ngo 1.99.0\n")

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "go.mod requires go >= 1.99.0")
	assert.Contains(t, note, "GOTOOLCHAIN=local")
	entries, err := os.ReadDir(cache)
	require.NoError(t, err)
	assert.Empty(t, entries, "no toolchain or module reached the cache")
}

func TestFill_ADownloadFailureIsANoteAndNeverStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newRig(t, `echo "verifying example.com/dep@v1.0.0: checksum mismatch" >&2; echo "second line" >&2; exit 1`)
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Equal(t, filepath.Join(r.filler.Root, "proj"), cache, "the step starts with the cache as it is")
	assert.Contains(t, note, "verifying example.com/dep@v1.0.0: checksum mismatch")
	assert.NotContains(t, note, "second line", "the note holds the first line of the error")
}

func TestFill_AFailureWithNoTextNamesTheExitCode(t *testing.T) {
	t.Parallel()

	r := newRig(t, `exit 3`)
	write(t, filepath.Join(r.work, "go.mod"), module)

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "exit code 3")
}

func TestFill_AnUnreadableGoModIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(r.work, "go.mod"), "this is not a go.mod\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "go.mod")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_AnEditCommandThatPrintsNoJSONIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, filepath.Join(filepath.Dir(r.calls), "edit.out"), "not json")
	write(t, filepath.Join(r.work, "go.mod"), module)

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "printed no JSON")
	assert.Empty(t, r.downloads(t), "no download ran")
}

func TestFill_NoGoCommandOnThePathIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	r.filler.Path = t.TempDir()
	write(t, filepath.Join(r.work, "go.mod"), module)

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, "no go command")
}

func TestFill_ACacheFolderThatCannotBeMadeIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	write(t, r.filler.Root, "a file, not a folder")
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Empty(t, cache)
	assert.Contains(t, note, r.filler.Root)
}

func TestFill_AHomeFolderThatCannotBeMadeIsANote(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	require.NoError(t, os.MkdirAll(filepath.Join(r.filler.Root, "proj"), 0o700))
	require.NoError(t, exectest.ChmodFolder(r.filler.Root, 0o500), "a read-only root refuses the temporary home")
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(r.filler.Root, 0o700)) })
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Equal(t, filepath.Join(r.filler.Root, "proj"), cache)
	assert.Contains(t, note, "home")
	assert.Empty(t, r.downloads(t), "no download ran")
}
