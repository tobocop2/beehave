package forge_test

import (
	"testing"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/model/contract"
)

func TestForge_ConformsToForge(t *testing.T) {
	t.Parallel()

	contract.TestForge(t, func(t *testing.T, broken bool) model.Forge {
		t.Helper()

		if broken {
			f, _ := failing(t, "boom")

			return f
		}

		run := &scripted{}
		run.plan(toJSON(t, []any{[]fields{{"user": fields{"login": contract.SevenReviewer}, "state": "APPROVED"}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/reviews")
		run.plan(contract.SevenBody, "pr", "view", "7", "-R")
		run.plan(toJSON(t, fields{"title": contract.SevenTitle}), "pr", "view", "7", "--repo")
		run.plan(toJSON(t, []fields{{"number": contract.Seven}}), "pr", "list")
		run.plan(toJSON(t, []fields{{"name": contract.SevenCheck, "bucket": "pass"}}), "pr", "checks")

		return newForge(t, repo, run)
	})
}
