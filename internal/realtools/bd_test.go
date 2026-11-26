package realtools_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// TestRealBd_StoreScenarios runs the contract tests of the beads store against the real bd 1.3.2 and its private dolt server: the claim lease and the item round trip.
// The scenarios live beside the fake bd in internal/store/beads, which keeps one copy of each. This test runs them with the pinned bd and fails when one is skipped.
func TestRealBd_StoreScenarios(t *testing.T) {
	requireSuite(t)

	if config.ProcessEnv()[beadstest.BinaryVariable] == "" {
		t.Skipf("%s is not set: scripts/install-real-tools.sh installs bd 1.3.2 and make test-real-tools names it", beadstest.BinaryVariable)
	}

	run := proc.NewExecRunner("go", "../..")
	run.Timeout = 0

	done := run.Run(t.Context(), []string{"test", "-count=1", "-v", "-run", "^(TestLeaseContract|TestStoreContract_ItemRoundTrip)$", "./internal/store/beads/"})
	require.Zero(t, done.ExitCode, "the contract tests of the beads store failed on the real bd:\n%s\n%s", done.Stdout, done.Stderr)

	assert.Contains(t, done.Stdout, "--- PASS: TestStoreContract_ItemRoundTrip/real_bd_1.3.2 ")
	assert.Contains(t, done.Stdout, "--- PASS: TestLeaseContract/real_bd_1.3.2 ")
	assert.NotRegexp(t, `--- SKIP: [^\n]*real_bd_1\.3\.2`, done.Stdout, "a scenario on the real bd was skipped")
}
