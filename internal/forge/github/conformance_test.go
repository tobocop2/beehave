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
		run.Plan(toJSON(t, fields{"commits": forgetest.SevenCommits, "changed_files": forgetest.SevenFiles}), "api", "repos/owner/name/pulls/7")
		run.Plan(toJSON(t, []fields{{"name": forgetest.SevenCheck, "bucket": "pass"}}), "pr", "checks")

		run.Plan(toJSON(t, []any{[]fields{{"body": forgetest.SevenComment}}}), "api", "--paginate", "--slurp", "repos/owner/name/issues/7/comments")
		run.Plan(toJSON(t, []any{[]fields{{"body": forgetest.SevenInline}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/comments")
		run.Plan(toJSON(t, []fields{{"number": forgetest.Seven}}), "search", "prs")
		run.Plan(toJSON(t, fields{"defaultBranchRef": fields{"name": forgetest.DefaultBranch}}), "repo", "view")
		run.Plan(toJSON(t, fields{"object": fields{"sha": forgetest.SevenTip}}), "api", "repos/owner/name/git/ref/heads/"+forgetest.SevenBranch)
		run.PlanFail(realNotFound, "api", "repos/owner/name/git/ref/heads/"+forgetest.GoneBranch)
		run.Plan("owner/name\n", "api", "repos/owner/name", "--jq", ".full_name")
		run.Plan(forgetest.SevenTitle+"\n", "issue", "view", "7")
		run.PlanContaining(toJSON(t, fields{"data": fields{"search": fields{"issueCount": 1, "nodes": []fields{{"number": forgetest.Seven}}}}}), "search(query")
		run.Plan("[]", "api", "repos/owner/name/check-runs/9/annotations")
		run.Plan("", "pr", "comment")
		run.Plan("", "pr", "ready")
		run.Plan("", "pr", "merge")
		run.Plan("", "pr", "edit")
		run.Plan("", "api", "-X", "POST", "repos/owner/name/git/refs")
		run.Plan("github.com\n  - Token scopes: '"+forgetest.ScopeRepo+"', '"+forgetest.ScopeWorkflow+"'\n", "auth", "status")

		return newForge(t, repo, run)
	})
}
