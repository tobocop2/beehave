package ifacetest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ifacetest"
)

func TestMethods_ListsTheMethodsOfAnInterfaceAndOfTheOnesItEmbeds(t *testing.T) {
	t.Parallel()

	got, err := ifacetest.Methods([]string{"testdata/sample.go"}, "Outer")

	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha", "Beta", "Gamma"}, got)
}

func TestMethods_AMissingInterfaceOrAFileThatDoesNotParseIsAnError(t *testing.T) {
	t.Parallel()

	_, err := ifacetest.Methods([]string{"testdata/sample.go"}, "Nope")
	require.ErrorContains(t, err, "no interface Nope")

	_, err = ifacetest.Methods([]string{"testdata/missing.go"}, "Outer")
	require.ErrorContains(t, err, "cannot parse")

	_, err = ifacetest.Methods([]string{"testdata/broken.go"}, "Outer")
	require.ErrorContains(t, err, "no interface Gone")
}
