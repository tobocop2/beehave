package brief_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
)

func writeFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestCheckSlot_AcceptsAPathAndANoneWithAReason(t *testing.T) {
	t.Parallel()

	require.NoError(t, brief.CheckSlot("acceptance", "tests/accept.sh", "fix-1"))
	require.NoError(t, brief.CheckSlot("acceptance", "none: a comment only", "fix-1"))
}

func TestCheckSlot_RefusesABadValueAndNamesTheSlot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, value, fragment string
	}{
		{"empty", "", "the lane fix-1 has no acceptance; give a path with the acceptance field"},
		{"none-no-reason", "none:", "the acceptance field says 'none:' with no reason"},
		{"none-blank-reason", "none: \t\n", "the acceptance field says 'none:' with no reason"},
		{"bare-none", "none", "is a placeholder"},
		{"bracket", "[PATH]", "is a placeholder"},
		{"angle", "<path>", "is a placeholder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := brief.CheckSlot("acceptance", tc.value, "fix-1")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.fragment)
		})
	}
}

func TestCheckSlot_AnEmptySlotNamesTheBaseResultToo(t *testing.T) {
	t.Parallel()

	err := brief.CheckSlot("base_result", "", "fix-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no base_result")
	assert.Contains(t, err.Error(), "give a path with the base_result field, or 'none: <reason>'")
}
