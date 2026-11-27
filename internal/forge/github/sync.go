package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	cmdMerges       = "gh api merges"
	conflictStatus  = "(HTTP 409)"
	compareAheadKey = ".ahead_by"
)

// mergeAnswer is the answer of GitHub to a branch merge: the merge commit. A merge with nothing to merge has no answer.
type mergeAnswer struct {
	SHA string `json:"sha"`
}

// CommitsAhead returns the number of commits of head that base lacks, as the compare endpoint counts them.
// https://docs.github.com/en/rest/commits/commits#compare-two-commits
func (f *Forge) CommitsAhead(ctx context.Context, base, head string) (int, error) {
	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/compare/" + base + "..." + head, argJQ, compareAheadKey})
	if done.ExitCode != 0 {
		return 0, forge.Refuse("cannot compare branch %s with %s: %s", head, base, firstLine(done.Stderr))
	}

	ahead, err := strconv.Atoi(strings.TrimSpace(done.Stdout))
	if err != nil || ahead < 0 {
		return 0, forge.Refuse("cannot read the number of commits that branch %s has and %s lacks", head, base)
	}

	return ahead, nil
}

// MergeBranch merges head into base with a merge commit and returns its sha. GitHub makes the merge commit also when base is an ancestor of head, and answers 204 with no body when base already holds head.
// A conflict is the answer 409, and it changes nothing.
// https://docs.github.com/en/rest/branches/branches#merge-a-branch
func (f *Forge) MergeBranch(ctx context.Context, base, head string) (string, error) {
	done := f.run.Run(ctx, []string{argAPI, "-X", argPost, "repos/" + f.repo + "/merges", "-f", "base=" + base, "-f", "head=" + head})
	if done.ExitCode != 0 {
		refusal := refused(done, "cannot merge branch %s into %s", head, base)
		if strings.Contains(done.Stderr, conflictStatus) {
			refusal.Err = forge.ErrMergeConflict
		}

		return "", refusal
	}

	return mergeSHA(done.Stdout, base, head)
}

// mergeSHA reads the merge commit from the answer of a merge. An empty answer is the 204 of a merge with nothing to merge.
func mergeSHA(stdout, base, head string) (string, error) {
	if strings.TrimSpace(stdout) == "" {
		return "", nil
	}

	answer, err := model.Decode[mergeAnswer](cmdMerges, stdout)
	if err != nil {
		return "", forge.Refusal("cannot read the merge of branch "+head+" into "+base, err)
	}

	if answer.SHA == "" {
		return "", forge.Refuse("cannot read the merge commit of branch %s into %s: the answer holds no sha", head, base)
	}

	return answer.SHA, nil
}
