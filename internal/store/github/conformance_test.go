package github_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/storetest"
)

func TestStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	storetest.TestTracker(t, func(t *testing.T, broken bool) store.Tracker {
		t.Helper()

		run := proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "boom"})
		if !broken {
			run.Plan(`{"number":5,"title":"A finding","body":"The body."}`, "issue", "view")
			run.Plan(`[{"data":{"repository":{"list":{"totalCount":1,"nodes":[{"number":5,"title":"A finding","labels":{"nodes":[{"name":"bug"}]},"assignees":{"nodes":[]},"authorAssociation":"OWNER"}]}}}}]`, "api", "graphql")
			run.Plan(`[{"data":{"repository":{"list":{"totalCount":0,"nodes":[]}}}}]`, "-f", "l[]=no-item-carries-this-label")
		}

		gh, err := github.New(ghRepo, run)
		require.NoError(t, err)

		return gh
	})
}
