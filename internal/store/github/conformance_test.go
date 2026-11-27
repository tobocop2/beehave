package github_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
	"github.com/tobocop2/beehest/internal/store"
	"github.com/tobocop2/beehest/internal/store/github"
	"github.com/tobocop2/beehest/internal/store/storetest"
)

func TestStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	storetest.TestTracker(t, func(t *testing.T, broken bool) store.Tracker {
		t.Helper()

		run := proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "boom"})
		if !broken {
			run.Plan(`{"number":5,"title":"A finding","body":"The body."}`, "issue", "view")
			run.Plan(`[{"number":5,"title":"A finding","labels":[{"name":"bug"}],"assignees":[]}]`, "issue", "list")
			run.Plan(`[]`, "--label", "no-item-carries-this-label")
		}

		gh, err := github.New(ghRepo, run)
		require.NoError(t, err)

		return gh
	})
}
