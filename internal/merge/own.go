package merge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// fields are what the first read asks for: every fact the guards decide on, in one call.
func fields() []string {
	return []string{"state", "isDraft", "baseRefName", "headRefName", "headRefOid", "mergeable", "files", "commits", "body", "author", "labels"}
}

// ownWork is guard 0. It reads the pull request once and refuses work of ours: the author of the pull request, or any author
// of any commit, whose login or address is an owner login, an owner address or the login of a runner. Case does not count.
// Only Approved lifts it, and it runs before any other read, so a refused pull request has had no write.
func (a *attempt) ownWork() *refusal {
	if !named(a.req.OwnLogins) || !named(a.req.OwnEmails) {
		return refuse("REFUSE: the owner logins and the owner addresses must both be set, and none may be empty. They tell obee which work is its own, and obee does not merge its own work without a verdict from another step.")
	}

	if stop := a.read(); stop != nil {
		return stop
	}

	who, role := owner(a.view, a.committers, ourNames(a.req.OwnLogins, a.req.OwnEmails, a.req.Runners))
	if who == "" || a.req.Approved {
		return nil
	}

	return ownRefusal(
		fmt.Sprintf("REFUSE: #%d holds work of %s (%s). obee does not merge its own work without a verdict from another step.", a.req.PR, who, role),
		"        The owner merges it in the forge, or the merge step sets `approved` from a review.",
	)
}

// named is true when the list has an entry and no entry is empty.
func named(names []string) bool {
	return len(names) > 0 && !slices.Contains(names, "")
}

func ownRefusal(lines ...string) *refusal {
	return &refusal{stop: OwnWork, lines: lines}
}

// read makes the first read and reads the forge's own counts. A commit list shorter than the count is refused, because a commit of ours could hide in the rest.
// The count of files is kept for the scope guard.
func (a *attempt) read() *refusal {
	view, err := a.forge.PRView(a.ctx, a.req.PR, a.asked()...)
	if err != nil {
		return transient(fmt.Sprintf("REFUSE: cannot read #%d for its state", a.req.PR))
	}

	a.view = view

	commits, files, err := a.forge.PullCounts(a.ctx, a.req.PR)
	if err != nil {
		return ownRefusal(fmt.Sprintf("REFUSE: the commits of #%d cannot be counted, so work of ours cannot be ruled out.", a.req.PR))
	}

	if commits > len(view.Commits) {
		return ownRefusal(fmt.Sprintf("REFUSE: the list of commits is incomplete for #%d (%d read, %d in the forge), so work of ours cannot be ruled out.",
			a.req.PR, len(view.Commits), commits))
	}

	a.files = files

	return a.readCommitters(commits)
}

// readCommitters reads the committer of each commit. The commit list of the first read names only authors.
// A list shorter than the count is refused, because a commit of ours could hide in the rest.
func (a *attempt) readCommitters(commits int) *refusal {
	committers, err := a.forge.Committers(a.ctx, a.req.PR)
	if err != nil {
		return ownRefusal(fmt.Sprintf("REFUSE: the committers of #%d cannot be read, so work of ours cannot be ruled out.", a.req.PR))
	}

	if commits > len(committers) {
		return ownRefusal(fmt.Sprintf("REFUSE: the list of committers is incomplete for #%d (%d read, %d in the forge), so work of ours cannot be ruled out.",
			a.req.PR, len(committers), commits))
	}

	a.committers = committers

	return nil
}

// ourNames returns the logins and the addresses that mark work as ours, in lower case.
func ourNames(logins, emails, runners []string) map[string]bool {
	own := map[string]bool{}

	for _, name := range slices.Concat(logins, emails, runners) {
		if name != "" {
			own[strings.ToLower(name)] = true
		}
	}

	return own
}

// owner returns the first login or address of the pull request that is ours, and whose it is.
// It looks at the author of the pull request, the authors of the commits, then the committers.
func owner(view model.PullRequest, committers []model.Committer, own map[string]bool) (who, role string) {
	if name := ours(own, view.Author.Login); name != "" {
		return name, "the author of the pull request"
	}

	for _, commit := range view.Commits {
		for _, author := range commit.Authors {
			if name := ours(own, author.Login, author.Email); name != "" {
				return name, "the author of a commit"
			}
		}
	}

	for _, committer := range committers {
		if name := ours(own, committer.Login, committer.Email); name != "" {
			return name, "the committer of a commit"
		}
	}

	return "", ""
}

// ours returns the first of the names that is ours, or an empty text. An empty name is never ours.
func ours(own map[string]bool, names ...string) string {
	for _, name := range names {
		if name != "" && own[strings.ToLower(name)] {
			return name
		}
	}

	return ""
}
