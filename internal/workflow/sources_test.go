package workflow_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// folder writes the files into a new folder and returns the path of the first one.
func folder(t *testing.T, files map[string]string, main string) string {
	t.Helper()

	dir := t.TempDir()

	for name, text := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}

	return filepath.Join(dir, main)
}

func paths(pinned []workflow.PinnedFile) []string {
	out := make([]string, len(pinned))
	for i, p := range pinned {
		out[i] = p.Path
	}

	return out
}

func TestPin_FollowsLoadsAndHashesTheEmbeddedLibrary(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{
		"workflow.star": "load(\"//lib/a.star\", \"a\")\nload(\"@beehave//review_fix.star\", \"review_fix\")\ndef main(item):\n    return a\n",
		"lib/a.star":    "load(\"//lib/b.star\", \"b\")\na = b\n",
		"lib/b.star":    "b = 1\n",
	}, "workflow.star")

	pinned, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"@beehave//review_fix.star", "lib/a.star", "lib/b.star", "workflow.star"}, paths(pinned))

	for _, p := range pinned {
		assert.Len(t, p.SHA256, 64, p.Path)
	}
}

func TestPin_RefusesFilesThatLoadEachOther(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{
		"workflow.star": "load(\"//a.star\", \"a\")\ndef main(item):\n    return a\n",
		"a.star":        "load(\"//b.star\", \"b\")\na = b\n",
		"b.star":        "load(\"//a.star\", \"a\")\nb = a\n",
	}, "workflow.star")

	_, err := workflow.Pin(file)

	require.ErrorContains(t, err, "files load each other: a.star -> b.star -> a.star")
}

func TestOpen_NamesTheFileThatChanged(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{
		"workflow.star": "load(\"//lib/a.star\", \"a\")\ndef main(item):\n    return a\n",
		"lib/a.star":    "a = 1\n",
	}, "workflow.star")

	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "lib", "a.star"), []byte("a = 2\n"), 0o600))

	_, err = workflow.Open(file, pinned)

	require.ErrorContains(t, err, "lib/a.star changed since the run started: it had hash")
	require.ErrorContains(t, err, "Restore the file, or cancel the run and start again")
}

func TestOpen_RefusesAFileThatIsGone(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    return 1\n"}, "workflow.star")

	pinned, err := workflow.Pin(file)
	require.NoError(t, err)
	require.NoError(t, os.Remove(file))

	_, err = workflow.Open(file, pinned)

	require.ErrorContains(t, err, "read workflow.star")
}

func TestOpen_RefusesAPinWithoutTheMainFile(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    return 1\n"}, "workflow.star")

	_, err := workflow.Open(file, nil)

	require.ErrorContains(t, err, "the run did not pin workflow.star")
}

func opened(t *testing.T, files map[string]string) (*workflow.Sources, string) {
	t.Helper()

	file := folder(t, files, "workflow.star")
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	sources, err := workflow.Open(file, pinned)
	require.NoError(t, err)

	return sources, file
}

func call(t *testing.T, sources *workflow.Sources) (starlark.Value, error) {
	t.Helper()

	thread := &starlark.Thread{Name: "test"}
	predeclared := starlark.StringDict{}

	for _, name := range workflow.Builtins() {
		predeclared[name] = starlark.NewBuiltin(name, nil)
	}

	main, err := sources.Entry(thread, predeclared, "main")
	if err != nil {
		return nil, err
	}

	return starlark.Call(thread, main, starlark.Tuple{starlark.MakeInt(2)}, nil)
}

func TestSourcesMain_RunsTheLoadedFilesFromThePinnedBytes(t *testing.T) {
	t.Parallel()

	sources, file := opened(t, map[string]string{
		"workflow.star": "load(\"//lib/a.star\", \"double\")\nload(\"//lib/a.star\", \"double2\")\ndef main(item):\n    return double(item) + double2(item)\n",
		"lib/a.star":    "def double(x):\n    return x * 2\n\ndouble2 = double\n",
	})

	// A change after Open does not reach the run: the bytes are held.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "lib", "a.star"), []byte("def double(x):\n    return 0\n"), 0o600))

	got, err := call(t, sources)

	require.NoError(t, err)
	assert.Equal(t, "8", got.String())
}

func TestSourcesMain_RunsTheEmbeddedLibrary(t *testing.T) {
	t.Parallel()

	sources, _ := opened(t, map[string]string{
		"workflow.star": "load(\"@beehave//review_fix.star\", \"FORBID\")\ndef main(item):\n    return len(FORBID)\n",
	})

	got, err := call(t, sources)

	require.NoError(t, err)
	assert.Equal(t, "10", got.String())
}

func TestSourcesMain_RefusesALoadOutsideThePin(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{
		"workflow.star": "load(\"//other.star\", \"x\")\ndef main(item):\n    return x\n",
		"other.star":    "x = 1\n",
	}, "workflow.star")

	only := []workflow.PinnedFile{}
	full, err := workflow.Pin(file)
	require.NoError(t, err)

	for _, p := range full {
		if p.Path == "workflow.star" {
			only = append(only, p)
		}
	}

	sources, err := workflow.Open(file, only)
	require.NoError(t, err)

	_, err = call(t, sources)

	require.ErrorContains(t, err, `the run did not pin "//other.star"`)
}

func TestSourcesMain_RefusesAFileWithoutMain(t *testing.T) {
	t.Parallel()

	_, err := workflow.Pin(folder(t, map[string]string{"workflow.star": "x = 1\n"}, "workflow.star"))

	require.ErrorContains(t, err, "defines no function main")
}

func TestSourcesMain_ReportsAFailedFileWithItsPosition(t *testing.T) {
	t.Parallel()

	sources, _ := opened(t, map[string]string{"workflow.star": "x = 1 // 0\ndef main(item):\n    return x\n"})

	_, err := call(t, sources)

	var failed *starlark.EvalError

	require.ErrorAs(t, err, &failed)
	assert.Equal(t, "workflow.star:1:7", failed.CallStack[0].Pos.String())
}

func TestSourcesMain_RefusesAFileWithoutMainThatIsPinned(t *testing.T) {
	t.Parallel()

	text := "x = 1\n"
	file := folder(t, map[string]string{"workflow.star": text}, "workflow.star")
	sum := sha256.Sum256([]byte(text))

	sources, err := workflow.Open(file, []workflow.PinnedFile{{Path: "workflow.star", SHA256: hex.EncodeToString(sum[:])}})
	require.NoError(t, err)

	_, err = sources.Entry(&starlark.Thread{Name: "test"}, starlark.StringDict{}, "main")

	require.ErrorContains(t, err, "defines no function main")
}

func TestSourcesEntry_NamesAFunctionOtherThanMainAndDefinesSeesOnlyFunctions(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "x = 1\ndef main(item):\n    return 1\ndef worker(item):\n    return 2\n"}, "workflow.star")
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	sources, err := workflow.Open(file, pinned)
	require.NoError(t, err)

	assert.False(t, sources.Defines("worker"), "nothing ran yet")

	thread := &starlark.Thread{Name: "test"}
	entry, err := sources.Entry(thread, starlark.StringDict{}, "worker")
	require.NoError(t, err)

	got, err := starlark.Call(thread, entry, starlark.Tuple{starlark.MakeInt(0)}, nil)
	require.NoError(t, err)
	assert.Equal(t, starlark.MakeInt(2), got)

	assert.True(t, sources.Defines("worker"))
	assert.True(t, sources.Defines("main"))
	assert.False(t, sources.Defines("x"), "a number is not a function")
	assert.False(t, sources.Defines("absent"))

	_, err = sources.Entry(thread, starlark.StringDict{}, "absent")
	require.ErrorContains(t, err, "defines no function absent; add def absent(item)")
}

func TestSourcesMain_ReportsAFailureInALoadedFile(t *testing.T) {
	t.Parallel()

	sources, _ := opened(t, map[string]string{
		"workflow.star": "load(\"//a.star\", \"x\")\ndef main(item):\n    return x\n",
		"a.star":        "x = 1 // 0\n",
	})

	_, err := call(t, sources)

	require.ErrorContains(t, err, "cannot load //a.star")
}

func TestSourcesMain_RefusesALoadOfAnUnknownPath(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    return 1\n"}, "workflow.star")
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	sources, err := workflow.Open(file, pinned)
	require.NoError(t, err)

	thread := &starlark.Thread{Name: "test"}
	_, err = sources.Entry(thread, starlark.StringDict{}, "main")
	require.NoError(t, err)

	_, err = thread.Load(thread, "../x.star")

	require.ErrorContains(t, err, `cannot load "../x.star"`)
}

func TestEnvNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"DBOS__APPVERSION", "BEEHAVE_STATE"} {
		assert.True(t, workflow.ReservedEnvName(name), name)
	}

	assert.False(t, workflow.ReservedEnvName("REPO"))
	assert.Contains(t, workflow.ReservedEnvAdvice(), "DBOS__")

	for _, name := range []string{"GITHUB_TOKEN", "my_secret", "DB_Password", "AWS_CREDENTIALS", "API_KEY", "api_key"} {
		assert.True(t, workflow.SecretEnvName(name), name)
	}

	for _, name := range []string{"REPO", "BASE", "KEYBOARD", "OWNER_LOGIN"} {
		assert.False(t, workflow.SecretEnvName(name), name)
	}
}

func TestStopCode(t *testing.T) {
	t.Parallel()

	assert.False(t, workflow.StopCode(0))
	assert.True(t, workflow.StopCode(1))
	assert.True(t, workflow.StopCode(255))
	assert.False(t, workflow.StopCode(256))
}
