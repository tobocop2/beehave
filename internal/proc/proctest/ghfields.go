package proctest

import (
	"slices"
	"strings"
)

// The --json fields that gh pr and gh issue list and view accept: sharedIssuePRFields, issueOnlyFields and PullRequestFields of api/query_builder.go
// in cli/cli v2.101.0, commit 0cf1092493af067646fc5f3db9421c6a6ec9c938.
const (
	ghSharedFields = "assignees,author,body,closed,comments,createdAt,closedAt,id,labels,milestone,number,projectCards,projectItems,reactionGroups,state,title,updatedAt,url"
	ghIssueFields  = ghSharedFields + ",isPinned,stateReason,closedByPullRequestsReferences,issueType,parent,subIssues,subIssuesSummary,blockedBy,blocking"
	ghPullFields   = ghSharedFields + ",additions,autoMergeRequest,baseRefName,baseRefOid,changedFiles,closingIssuesReferences,commits,deletions,files," +
		"fullDatabaseId,headRefName,headRefOid,headRepository,headRepositoryOwner,isCrossRepository,isDraft,latestReviews,maintainerCanModify,mergeable," +
		"mergeCommit,mergedAt,mergedBy,mergeStateStatus,potentialMergeCommit,reviewDecision,reviewRequests,reviews,statusCheckRollup"
	jsonFlag = "--json"
)

// unknownGHField is the first --json field of a gh pr or gh issue list or view that gh does not accept. It is empty for any other call.
func unknownGHField(args []string) string {
	known, read := map[string]string{"pr": ghPullFields, "issue": ghIssueFields}[first(args)]
	at := slices.Index(args, jsonFlag)

	if !read || len(args) < 2 || (args[1] != "list" && args[1] != "view") || at < 0 || at+1 >= len(args) {
		return ""
	}

	accepted := strings.Split(known, ",")
	for _, field := range strings.Split(args[at+1], ",") {
		if !slices.Contains(accepted, field) {
			return field
		}
	}

	return ""
}

// first is the first argument, or empty for none.
func first(args []string) string {
	if len(args) == 0 {
		return ""
	}

	return args[0]
}
