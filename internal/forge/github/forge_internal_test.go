package github

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge"
)

func TestIntegrationAgrees(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{{"", ""}, {"a", ""}, {"", "a"}, {"a", "a"}} {
		require.NoError(t, integrationAgrees(pair[0], pair[1]), pair)
	}

	err := integrationAgrees("main", "stage")
	require.Error(t, err)
	assert.Equal(t, []string{
		"BASE is main and INTEGRATION_BRANCH is stage. BASE must equal INTEGRATION_BRANCH.",
		"        Both name the branch that pull requests target. Keep the branch that pull requests target, and set both to it in round.env.",
		"        Or unset INTEGRATION_BRANCH to turn the check off.",
	}, strings.Split(err.Error(), "\n"))

	var refused *forge.Error

	require.ErrorAs(t, err, &refused)
	assert.Equal(t, forge.CodeRefused, refused.Code)
	assert.NoError(t, refused.Unwrap())
}
