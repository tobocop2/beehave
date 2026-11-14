package skyslurm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestTeardown_TheBankScanHonoursTheConfiguredBudget(t *testing.T) {
	t.Parallel()

	cfg := hostConfig()
	cfg.BankBudget = 50 * time.Millisecond

	ssh := &proctest.Scripted{}
	planHome(ssh)

	h, err := newHost("lane-host", cfg, &proctest.Scripted{}, hosttest.HangingGit(ssh, "/home/lane/repo"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err = h.Teardown(ctx)

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "the time budget of 50ms ended")
	assert.Less(t, time.Since(start), 2*time.Second, "the configured budget, not the caller context, ends the wait")
}
