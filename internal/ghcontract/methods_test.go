package ghcontract_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
)

const methodsSource = `package sample

type Thing struct{}

type Other struct{}

type Boxed[T any] struct{}

func (t *Thing) Zeta()    {}
func (t Thing) Alpha()    {}
func (t *Thing) hidden()  {}
func (o *Other) Foreign() {}
func (b *Boxed[T]) Generic() {}
func Free()               {}
`

const methodsMore = `package sample

func (t *Thing) Middle() {}
`

const methodsTest = `package sample

func (t *Thing) OnlyInTests() {}
`

func writeSample(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	for name, text := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
	}

	return dir
}

func TestPublicMethods_ListsTheExportedMethodsOfOneTypeSorted(t *testing.T) {
	t.Parallel()

	dir := writeSample(t, map[string]string{"a.go": methodsSource, "b.go": methodsMore, "a_test.go": methodsTest, "notes.txt": "Thing"})
	require.NoError(t, os.Mkdir(filepath.Join(dir, "folder.go"), 0o700))

	got, err := ghcontract.PublicMethods(dir, "Thing")
	require.NoError(t, err)

	assert.Equal(t, []string{"Alpha", "Middle", "Zeta"}, got, "a pointer receiver and a value receiver count; a hidden method, a test file, another type and a generic receiver do not")
}

func TestPublicMethods_RefusesAFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()

	_, err := ghcontract.PublicMethods(filepath.Join(t.TempDir(), "missing"), "Thing")

	assert.ErrorContains(t, err, "read ")
}

func TestPublicMethods_RefusesAFileThatDoesNotParse(t *testing.T) {
	t.Parallel()

	_, err := ghcontract.PublicMethods(writeSample(t, map[string]string{"broken.go": "package"}), "Thing")

	assert.ErrorContains(t, err, "parse broken.go")
}
