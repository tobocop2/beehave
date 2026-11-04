package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

func TestUnknownProject_NamesTheProjectAndListsTheConfigProjectsSorted(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Projects: map[string]config.Project{"q": {}, "a": {}, "m": {}}}

	require.EqualError(t, unknownProject("zzz", cfg), `no project "zzz" in the config: use one of a, m, q`)
}
