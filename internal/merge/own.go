package merge

import (
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// fields are what the first read asks for: every fact the guards decide on, in one call.
func fields() []string {
	return []string{"state", "isDraft", "baseRefName", "headRefName", "headRefOid", "mergeable", "files", "commits", "body", "author"}
}

// ownWork is guard 0. It reads the pull request once and refuses work of ours: the author of the pull request, or any author
// of any commit, whose login or address is the owner login, the owner address or the login of a runner. Case does not count.
// No setting lifts it, and it runs before any other read, so a refused pull request has had no write.
func (a *attempt) ownWork() *refusal {
	login, email := a.req.Settings.MeLogin(), a.req.Settings.MeEmail()
	if login == "" || email == "" {
		return refuse("REFUSE: ME_EMAIL and ME_LOGIN must both be set. They tell obee which work is its own, and obee never merges its own work.")
	}

	if stop := a.read(); stop != nil {
		return stop
	}

	if who, role := owner(a.view, ourNames(login, email, a.req.Runners)); who != "" {
		return ownRefusal(
			fmt.Sprintf("REFUSE: #%d holds work of %s (%s). obee never merges its own work.", a.req.PR, who, role),
			"        The owner merges it in the forge.",
		)
	}

	return nil
}

func ownRefusal(lines ...string) *refusal {
	return &refusal{stop: OwnWork, lines: lines}
}

// read makes the first read and reads the forge's own counts. A commit list shorter than the count is refused, because a commit of ours could hide in the rest.
// The count of files is kept for the scope guard.
func (a *attempt) read() *refusal {
	view, err := a.forge.PRView(a.ctx, a.req.PR, fields()...)
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read #%d for its state", a.req.PR))
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

	return nil
}

// ourNames returns the logins and the address that mark work as ours, in lower case.
func ourNames(login, email string, runners []string) map[string]bool {
	own := map[string]bool{strings.ToLower(login): true, strings.ToLower(email): true}

	for _, runner := range runners {
		if runner != "" {
			own[strings.ToLower(runner)] = true
		}
	}

	return own
}

// owner returns the first login or address of the pull request that is ours, and whose it is.
func owner(view model.PullRequest, own map[string]bool) (who, role string) {
	if view.Author.Login != "" && own[strings.ToLower(view.Author.Login)] {
		return view.Author.Login, "the author of the pull request"
	}

	for _, commit := range view.Commits {
		for _, author := range commit.Authors {
			for _, name := range []string{author.Login, author.Email} {
				if name != "" && own[strings.ToLower(name)] {
					return name, "the author of a commit"
				}
			}
		}
	}

	return "", ""
}
