package main

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
)

// Each library line of the list gives its file with links resolved, the vDSO line names no file, and the cache of the linker comes last when it exists.
func TestLddLibraries_GivesTheFileOfEachLibraryWithItsLinksResolvedAndTheCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lib, linker, cache := filepath.Join(dir, "libz.so.1.3"), filepath.Join(dir, "ld.so"), filepath.Join(dir, "ld.so.cache")

	for _, path := range []string{lib, linker, cache} {
		require.NoError(t, os.WriteFile(path, nil, 0o600))
	}

	require.NoError(t, os.Symlink(lib, filepath.Join(dir, "libz.so.1")))

	out := "\tlinux-vdso.so.1 (0x00007ffd)\n\tlibz.so.1 => " + filepath.Join(dir, "libz.so.1") + " (0x00007f00)\n\t" + linker + " (0x00007f01)\n"

	found, err := lddLibraries(out, cache)
	require.NoError(t, err)
	assert.Equal(t, []string{evaluated(t, lib), evaluated(t, linker), cache}, found)

	found, err = lddLibraries(out, filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Equal(t, []string{evaluated(t, lib), evaluated(t, linker)}, found, "a machine with no cache reads none")
}

// A library that the linker does not find, or a file that is gone, gives no read set.
func TestLddLibraries_ALibraryThatCannotBeFoundIsAnError(t *testing.T) {
	t.Parallel()

	for out, want := range map[string]string{
		"\tlibpcre2-8.so.0 => not found\n":        "the dynamic linker finds no file",
		"\t/nonexistent/libz.so.1 (0x00007f00)\n": "find the library /nonexistent/libz.so.1",
	} {
		_, err := lddLibraries(out, linkerCache)
		assert.ErrorContains(t, err, want, out)
	}
}

// A static git loads no library, so it reads no library and no cache; a file that is no ELF program gives no read set.
func TestGitLibraries_AStaticGitReadsNoLibraryAndAFileThatIsNoProgramIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source, static := filepath.Join(dir, "main.go"), filepath.Join(dir, "static")
	require.NoError(t, os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o600))

	// A program that links no C code is static whatever CGO_ENABLED says.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", static, source) //nolint:gosec // the paths are the temporary folder of the test
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	found, err := gitLibraries(t.Context(), static)
	require.NoError(t, err)
	assert.Empty(t, found)

	_, err = gitLibraries(t.Context(), source)
	assert.ErrorContains(t, err, "read the program")
}

// A dynamic git reads the dynamic linker that its program names, and the cache of the linker.
func TestGitLibraries_ADynamicProgramReadsItsLinkerAndTheCache(t *testing.T) {
	t.Parallel()

	found, err := gitLibraries(t.Context(), "/bin/sh")
	require.NoError(t, err)
	assert.Contains(t, found, linkerCache)
	assert.NotEmpty(t, found[:len(found)-1], "the linker and the libraries")
}

// A cache of the linker that cannot be read, other than one that does not exist, gives no read set.
func TestLddLibraries_ACacheThatCannotBeStatedIsAnError(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := lddLibraries("", filepath.Join(file, "ld.so.cache"))
	assert.ErrorContains(t, err, "read the cache of the linker")
}

// A dynamic linker that cannot run gives no read set.
func TestGitLibraries_ALinkerThatCannotRunIsAnError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := gitLibraries(ctx, "/bin/sh")
	assert.ErrorContains(t, err, "list the libraries")
}

// The libraries come from the dynamic linker that the program names, run by its path, so a search path with no ldd, as the one of the engine can be, finds
// them all the same.
func TestGitLibraries_ASearchPathWithNoLddFindsTheLibraries(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	found, err := gitLibraries(t.Context(), "/bin/sh")
	require.NoError(t, err)

	interpreter, err := programInterpreter("/bin/sh")
	require.NoError(t, err)
	assert.Contains(t, found, evaluated(t, interpreter), "the dynamic linker is read")
	assert.Greater(t, len(found), 2, "the linker, a library and the cache")
}

// withInterpreter returns a copy of /bin/sh in a new folder whose program header names interpreter as its dynamic linker.
func withInterpreter(t *testing.T, interpreter string) string {
	t.Helper()

	data, err := os.ReadFile("/bin/sh")
	require.NoError(t, err)

	file, err := elf.Open("/bin/sh")
	require.NoError(t, err)

	defer file.Close()

	for _, header := range file.Progs {
		if header.Type == elf.PT_INTERP {
			require.Less(t, uint64(len(interpreter)), header.Filesz, "the new name fits the old one")
			field := data[header.Off : header.Off+header.Filesz]
			clear(field)
			copy(field, interpreter)
		}
	}

	program := filepath.Join(t.TempDir(), "sh")
	require.NoError(t, exectest.Write(program, string(data)))

	return program
}

// shortDir returns a new folder with a short path, so that a linker in it fits the program header of /bin/sh.
func shortDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "b")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// A dynamic linker named by a link is run and read as the file that the link names.
func TestGitLibraries_ALinkerNamedByALinkIsReadAsItsFile(t *testing.T) {
	t.Parallel()

	real, err := programInterpreter("/bin/sh")
	require.NoError(t, err)

	link := filepath.Join(shortDir(t), "ld")
	require.NoError(t, os.Symlink(real, link))

	found, err := gitLibraries(t.Context(), withInterpreter(t, link))
	require.NoError(t, err)
	assert.Contains(t, found, evaluated(t, real))
	assert.NotContains(t, found, link)
}

// A dynamic linker named by a relative path, or one that is gone, gives no read set.
func TestGitLibraries_ALinkerThatIsRelativeOrGoneIsAnError(t *testing.T) {
	t.Parallel()

	for interpreter, want := range map[string]string{
		"lib/ld.so":          "by a relative path",
		"/nonexistent/ld.so": "find the dynamic linker",
	} {
		_, err := gitLibraries(t.Context(), withInterpreter(t, interpreter))
		assert.ErrorContains(t, err, want, interpreter)
	}
}

// The musl linker reads its list of library folders from /etc/ld-musl-<arch>.path, and the glibc linker from /etc/ld.so.cache.
func TestLinkerConfig_NamesTheFileThatTheLinkerReadsToFindALibrary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/etc/ld-musl-x86_64.path", linkerConfig("/lib/ld-musl-x86_64.so.1"))
	assert.Equal(t, "/etc/ld-musl-aarch64.path", linkerConfig("/lib/ld-musl-aarch64.so.1"))
	assert.Equal(t, linkerCache, linkerConfig("/lib64/ld-linux-x86-64.so.2"))
}

// A linker that lists a library it cannot find, or that exits with an error, gives no read set.
func TestGitLibraries_ALinkerThatFindsNoLibraryOrFailsIsAnError(t *testing.T) {
	t.Parallel()

	for script, want := range map[string]string{
		"#!/bin/sh\nprintf '\\tlibx.so.1 => not found\\n'\n": "the dynamic linker finds no file",
		"#!/bin/sh\nexit 1\n":                                "list the libraries",
	} {
		linker := filepath.Join(shortDir(t), "ld")
		require.NoError(t, exectest.Write(linker, script))

		_, err := gitLibraries(t.Context(), withInterpreter(t, linker))
		assert.ErrorContains(t, err, want, script)
	}
}
