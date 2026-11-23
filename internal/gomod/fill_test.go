package gomod_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700)) //nolint:gosec // the fake go command must be executable

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
	assert.Equal(t, r.work, r.downloads(t)[0]["DIR"], "the download runs in the item worktree")
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
	assert.Equal(t, filepath.Join(r.work, "go.work"), r.downloads(t)[0]["GOWORK"])
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

func TestFill_ALinkInTheWorktreeToAFolderOutsideIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, fillsCache)
	outside := evaluated(t, t.TempDir())
	write(t, filepath.Join(outside, "go.mod"), "module example.com/o\n\ngo 1.22\n")
	require.NoError(t, os.Symlink(outside, filepath.Join(r.work, "o")))
	write(t, filepath.Join(r.work, "go.work"), "go 1.22\n\nuse ./o\n")

	_, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Contains(t, note, outside)
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
	require.NoError(t, os.Chmod(r.filler.Root, 0o500))                      //nolint:gosec // a read-only root refuses the temporary home
	t.Cleanup(func() { assert.NoError(t, os.Chmod(r.filler.Root, 0o700)) }) //nolint:gosec // the temp folder cleanup needs write access back
	write(t, filepath.Join(r.work, "go.mod"), module)

	cache, note := r.filler.Fill(t.Context(), "proj", r.work)

	assert.Equal(t, filepath.Join(r.filler.Root, "proj"), cache)
	assert.Contains(t, note, "home")
	assert.Empty(t, r.downloads(t), "no download ran")
}
