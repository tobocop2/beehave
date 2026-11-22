package github

import (
	"context"
	"slices"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// maxStatusDescription is the longest description that GitHub accepts for a commit status.
	maxStatusDescription = 140
	ruleTypePull         = "pull_request"
	ruleTypeStatuses     = "required_status_checks"
)

// ruleAnswer is one rule that applies to a branch, as the rules endpoint lists it.
type ruleAnswer struct {
	Type       string `json:"type"`
	Parameters struct {
		Checks []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

// PostStatus sets a commit status on the commit that status.SHA names. The commit id must be the full 40 characters, and the context, a known state
// and a description of at most 140 characters are checked before gh runs.
// https://docs.github.com/en/rest/commits/statuses#create-a-commit-status
func (f *Forge) PostStatus(ctx context.Context, status model.CommitStatus) error {
	if err := checkStatus(status); err != nil {
		return err
	}

	args := []string{argAPI, "-X", argPost, "repos/" + f.repo + "/statuses/" + status.SHA, "-f", "state=" + string(status.State), "-f", "context=" + status.Context}
	if status.Description != "" {
		args = append(args, "-f", "description="+status.Description)
	}

	done := f.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return refused(done, "cannot post the status %s on commit %s", status.Context, status.SHA)
	}

	return nil
}

// checkStatus refuses a status that GitHub would refuse, or that would stand for no commit.
func checkStatus(status model.CommitStatus) error {
	switch {
	case !commitID.MatchString(status.SHA):
		return forge.Refuse("cannot post the status %q without the full commit id", status.Context)
	case status.Context == "":
		return forge.Refuse("cannot post a status with no context on commit %s", status.SHA)
	case !slices.Contains([]model.StatusState{model.StatusSuccess, model.StatusFailure, model.StatusPending, model.StatusError}, status.State):
		return forge.Refuse("cannot post the status %q on commit %s: %q is not a state of a status", status.Context, status.SHA, status.State)
	case len(status.Description) > maxStatusDescription:
		return forge.Refuse("cannot post the status %q on commit %s: the description is longer than %d characters", status.Context, status.SHA, maxStatusDescription)
	}

	return nil
}

// Protection reads the rules that apply to a branch, from every ruleset that matches it: whether a pull request is required, and the contexts of the required statuses.
// A branch that no rule matches has an empty protection.
// https://docs.github.com/en/rest/repos/rules#get-rules-for-a-branch
func (f *Forge) Protection(ctx context.Context, branch string) (model.Protection, error) {
	rules, err := readPages[ruleAnswer](ctx, f, "rules/branches/"+escapeSegments(branch))
	if err != nil {
		return model.Protection{}, forge.Refusal("cannot read the rules of the branch "+branch, err)
	}

	protection := model.Protection{RequiredStatuses: []string{}}

	for _, rule := range rules {
		switch rule.Type {
		case ruleTypePull:
			protection.PullRequest = true
		case ruleTypeStatuses:
			protection.RequiredStatuses = rule.addTo(protection.RequiredStatuses)
		}
	}

	return protection, nil
}

// addTo adds the contexts of a required statuses rule to the list, once each.
func (r ruleAnswer) addTo(contexts []string) []string {
	for _, check := range r.Parameters.Checks {
		if !slices.Contains(contexts, check.Context) {
			contexts = append(contexts, check.Context)
		}
	}

	return contexts
}
