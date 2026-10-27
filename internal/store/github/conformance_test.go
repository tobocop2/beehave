package github_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/github"
	"github.com/tobocop2/obee/internal/store/storetest"
)

func TestStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	storetest.TestTracker(t, func(t *testing.T, _ bool) store.Tracker {
		t.Helper()

		gh, err := github.New(ghRepo, proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "boom"}))
		require.NoError(t, err)

		return gh
	})
}
