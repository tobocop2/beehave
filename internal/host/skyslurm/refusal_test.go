package skyslurm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/skypilot"
)

func TestStatus_ADownNodeRefusesAndNamesTheFix(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"down\n", "down*\n", "idle\ndown~\n"} {
		h := upHost(t, text)

		_, err := h.Status(context.Background())

		require.ErrorContains(t, err, "down")
		require.ErrorContains(t, err, "State=RESUME", text)
	}
}

// upHost returns a host whose cluster is up and whose sinfo prints text.
func upHost(t *testing.T, text string) *skypilot.Host {
	t.Helper()

	h, sky, ssh := newScripted(t)
	sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")
	ssh.Plan(text, "sinfo")
	ssh.Plan(fakeDF, "df")

	return h
}
