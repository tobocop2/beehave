package skypilot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_NamesTheStateOfAClusterThatIsNotUp(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"INIT", "STOPPED"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			h, sky, ssh := newScripted(t)
			sky.Plan(`[{"name": "lane-host", "status": "`+state+`"}]`, "status")

			_, err := h.Status(context.Background())

			require.ErrorIs(t, err, errClusterUp)
			require.ErrorContains(t, err, state)
			assert.Empty(t, ssh.Calls, "no command runs on a cluster that is not up")
		})
	}
}

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
func upHost(t *testing.T, text string) *Host {
	t.Helper()

	h, sky, ssh := newScripted(t)
	sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")
	ssh.Plan(text, "sinfo")
	ssh.Plan(fakeDF, "df")

	return h
}
