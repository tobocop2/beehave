package remedy_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
)

func TestOpenForge_GivesTheForgeThatTheBackendOpened(t *testing.T) {
	t.Parallel()

	world := &forgeWorld{}
	open := remedy.OpenForge(func(string) (*forgeWorld, error) { return world, nil })

	got, err := open("owner/name")

	require.NoError(t, err)
	assert.Same(t, world, got)
}

func TestOpenForge_FailedOpenGivesNoForgeAtAll(t *testing.T) {
	t.Parallel()

	open := remedy.OpenForge(func(string) (*forgeWorld, error) { return nil, errDown })

	got, err := open("owner/name")

	require.ErrorIs(t, err, errDown)
	// A typed nil pointer in the interface would pass a nil check of the caller. The result must be the nil interface.
	assert.True(t, isNilInterface(got))
}

func TestOpenClaims_GivesTheClaimStoreThatTheOpenerOpened(t *testing.T) {
	t.Parallel()

	store := newClaimStore()
	open := remedy.OpenClaims(func(string) (*claimStore, error) { return store, nil })

	got, err := open("owner/name")

	require.NoError(t, err)
	assert.Same(t, store, got)
}

func TestOpenClaims_FailedOpenGivesNoClaimStoreAtAll(t *testing.T) {
	t.Parallel()

	open := remedy.OpenClaims(func(string) (*claimStore, error) { return nil, errDown })

	got, err := open("owner/name")

	require.ErrorIs(t, err, errDown)
	assert.True(t, isNilInterface(got))
}

// isNilInterface is true only for the nil interface. assert.Nil also accepts a typed nil pointer, which is the defect that OpenForge prevents.
func isNilInterface(value any) bool { return value == nil }

func TestMain_RunsTheCommandLineWithTheForgeOfTheCaller(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	code := remedy.Main([]string{"--help"}, &out, &errOut, func(string) (*forgeWorld, error) { return nil, errDown }, nil)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out.String(), "usage: remedy")
	assert.Empty(t, errOut.String())
}
