package github_test

import (
	"strconv"
	"testing"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/forge/forgetest"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

func TestForge_ConformsToForge(t *testing.T) {
	t.Parallel()

	forgetest.TestForge(t, conformanceForge)
}

func TestPRClosing_ReturnsTheIssuesAPullRequestCloses(t *testing.T) {
	t.Parallel()

	forgetest.CheckClosingRefs(t, conformanceForge(t, false))
}

func TestIssueState_ReturnsOpenOrClosed(t *testing.T) {
	t.Parallel()

	forgetest.CheckIssueState(t, conformanceForge(t, false))
}

// conformanceForge is the forge of the conformance suite over a fake command line that serves pull request 7, or one whose every command fails when broken is true.
func conformanceForge(t *testing.T, broken bool) forge.Forge {
	t.Helper()

	if broken {
		f, _ := failing(t, "boom")

		return f
	}

	return conformanceServing(t)
}

// conformanceServing builds the forge over the fake command line that answers every read of the suite.
func conformanceServing(t *testing.T) forge.Forge {
	t.Helper()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []any{[]fields{{"user": fields{"login": forgetest.SevenReviewer}, "state": "APPROVED"}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/reviews")
	run.Plan(forgetest.SevenBody, "pr", "view", "7", "-R")
	run.Plan(toJSON(t, fields{
		"title": forgetest.SevenTitle, "number": forgetest.Seven, "headRefName": forgetest.SevenBranch, "headRefOid": forgetest.SevenTip, "author": fields{"login": forgetest.SevenReviewer}, "state": forgetest.SevenState, "baseRefName": forgetest.SevenBase, "assignees": []fields{{"login": forgetest.SevenAssignee}},
		"isDraft": true, "labels": []fields{{"name": forgetest.SevenHold}}, "body": forgetest.SevenBody, "reviews": []fields{}, "reviewRequests": []fields{{"login": forgetest.SevenAsked}},
	}), "pr", "view", "7", "--repo")

	whole := func(pr int, drop string) {
		view := fields{"number": pr, "state": forgetest.SevenState, "headRefOid": forgetest.SevenTip, "baseRefName": forgetest.SevenBase, "isDraft": false, "labels": []fields{}, "assignees": []fields{}}
		delete(view, drop)
		run.Plan(toJSON(t, view), "pr", "view", strconv.Itoa(pr), "--repo")
	}
	whole(forgetest.NoHeadPull, "headRefOid")
	whole(forgetest.NoStatePull, "state")
	whole(forgetest.NoBasePull, "baseRefName")
	whole(forgetest.NoDraftPull, "isDraft")
	whole(forgetest.NoLabelsPull, "labels")
	whole(forgetest.NoAssigneesPull, "assignees")
	run.Plan(toJSON(t, []fields{{"number": forgetest.Seven}}), "pr", "list")
	seven := fields{"number": forgetest.Seven, "headRefName": forgetest.SevenBranch, "baseRefName": forgetest.SevenBase, "author": fields{"login": forgetest.SevenReviewer}, "isDraft": false, "title": forgetest.SevenTitle, "labels": []fields{{"name": forgetest.SevenLabel}}, "reviewDecision": "APPROVED"}
	other := fields{"number": forgetest.OtherPull, "headRefName": "other", "baseRefName": forgetest.OtherBase, "author": fields{"login": forgetest.SevenAssignee}, "isDraft": true, "title": "Other work", "labels": []fields{}, "reviewDecision": ""}
	run.Plan(toJSON(t, []fields{seven, other}), "--state", "open", "--json", "number,headRefName,baseRefName,labels,reviewDecision,author,isDraft,title")
	run.Plan(toJSON(t, []fields{seven}), "--base", forgetest.SevenBase)
	run.Plan(toJSON(t, []fields{other}), "--base", forgetest.OtherBase)
	run.Plan(toJSON(t, []fields{}), "--base", forgetest.EmptyBase)
	run.Plan(toJSON(t, []fields{{"number": forgetest.OtherPull, "baseRefName": forgetest.HoleBase}}), "--base", forgetest.HoleBase)
	run.Plan(toJSON(t, fields{"commits": forgetest.SevenCommits, "changed_files": forgetest.SevenFiles}), "api", "repos/owner/name/pulls/7")
	run.Plan(toJSON(t, []any{[]fields{{"commit": fields{"committer": fields{"email": "a@x"}}}, {"committer": fields{"login": "b"}}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/commits")
	run.Plan(toJSON(t, []fields{{"name": forgetest.SevenCheck, "bucket": "pass"}}), "pr", "checks")
	run.Plan(answer(t, forgetest.SevenBase, repo, issueList(2, issue(forgetest.SevenClosing, repo), issue(3, "Other-Org/Other-Repo"))), "-F", "p="+strconv.Itoa(forgetest.Seven))
	run.Plan(answer(t, forgetest.SevenBase, repo, issueList(0)), "-F", "p="+strconv.Itoa(forgetest.NoClosingPull))

	run.Plan(toJSON(t, []any{[]fields{{"body": forgetest.SevenComment}}}), "api", "--paginate", "--slurp", "repos/owner/name/issues/7/comments")
	run.Plan(toJSON(t, []any{[]fields{{"body": forgetest.SevenInline}}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/comments")
	run.Plan(toJSON(t, []fields{{"number": forgetest.Seven}}), "search", "prs")
	run.Plan(toJSON(t, fields{"defaultBranchRef": fields{"name": forgetest.DefaultBranch}}), "repo", "view")
	run.Plan(toJSON(t, fields{"object": fields{"sha": forgetest.SevenTip}}), "api", "repos/owner/name/git/ref/heads/"+forgetest.SevenBranch)
	run.PlanFail(realNotFound, "api", "repos/owner/name/git/ref/heads/"+forgetest.GoneBranch)
	run.Plan("owner/name\n", "api", "repos/owner/name", "--jq", ".full_name")
	run.Plan(forgetest.SevenTitle+"\n", "issue", "view", "7")
	run.Plan(forgetest.IssueOpen+"\n", "issue", "view", strconv.Itoa(forgetest.SevenIssue), "-R", repo, "--json", "state")
	run.Plan(forgetest.IssueClosed+"\n", "issue", "view", strconv.Itoa(forgetest.ClosedIssue), "-R", repo, "--json", "state")
	run.PlanContaining(searchAnswer(t, 1, fields{"number": forgetest.Seven}), "search(query")
	run.Plan("[]", "api", "repos/owner/name/check-runs/9/annotations")
	run.Plan("", "pr", "comment")
	run.Plan("", "pr", "ready")
	run.Plan("", "pr", "merge")
	run.Plan("", "pr", "edit")
	run.Plan(forgetest.SevenReviewer+"\n", "api", "user")
	run.Plan("", "api", "-X", "POST", "repos/owner/name/git/refs")
	run.Plan("", "api", "-X", "POST", "repos/owner/name/issues/7/assignees")
	run.Plan(authJSON(t, fields{"scopes": forgetest.ScopeRepo + ", " + forgetest.ScopeWorkflow}), "auth", "status")

	return newForge(t, repo, run)
}
