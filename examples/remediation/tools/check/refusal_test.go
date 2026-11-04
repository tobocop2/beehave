package check_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
)

func TestRefusal_ErrorIsTheMessage(t *testing.T) {
	t.Parallel()

	var err error = &check.Refusal{Message: "cannot run", Lines: []string{"found"}}

	require.EqualError(t, err, "cannot run")
}
