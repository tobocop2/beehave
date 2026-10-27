package github_test

import (
	"testing"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/forge/forgetest"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestForge_ConformsToForge(t *testing.T) {
	t.Parallel()

	forgetest.TestForge(t, func(t *testing.T, broken bool) forge.Forge {
		t.Helper()

		if broken {
			f, _ := failing(t, "boom")

			return f
		}

		run := &proctest.Scripted{}
		run.Plan(toJSON(t, []any{[]fields{{"user": fields{"login": forgetest.SevenReviewer}, "state": "APPROVED"}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/reviews")
		run.Plan(forgetest.SevenBody, "pr", "view", "7", "-R")
		run.Plan(toJSON(t, fields{"title": forgetest.SevenTitle}), "pr", "view", "7", "--repo")
		run.Plan(toJSON(t, []fields{{"number": forgetest.Seven}}), "pr", "list")
		run.Plan(toJSON(t, []fields{{"name": forgetest.SevenCheck, "bucket": "pass"}}), "pr", "checks")

		return newForge(t, repo, run)
	})
}
