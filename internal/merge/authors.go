package merge

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// shaWidth is how many characters of a commit id a line of an author shows.
const shaWidth = 9

// noLogin stands for a commit author with no linked account, and for a missing address.
const noLogin = "?"

// attribution refuses a line of the body, a commit headline or a commit body that matches a pattern of the request, and names each line.
// A line number counts from the first line of the body, then the commit bodies, then the headlines. With no pattern, nothing is forbidden.
func (a *attempt) attribution() *refusal {
	hits := forbiddenLines(a.view, a.req.ForbidText)
	if len(hits) == 0 {
		return nil
	}

	return refuse(append([]string{fmt.Sprintf("REFUSE: forbidden text on #%d. Offending lines:", a.req.PR)}, hits...)...)
}

func forbiddenLines(view model.PullRequest, forbidden []*regexp.Regexp) []string {
	var bodies, headlines []string

	for _, commit := range view.Commits {
		bodies = append(bodies, commit.MessageBody)
		headlines = append(headlines, commit.MessageHeadline)
	}

	whole := view.Body + "\n" + strings.Join(append(bodies, headlines...), "\n") + "\n"

	var hits []string

	for number, line := range strings.Split(whole, "\n")[:strings.Count(whole, "\n")] {
		if _, found := matchingRule(forbidden, line); found {
			hits = append(hits, fmt.Sprintf("  %d:%s", number+1, line))
		}
	}

	return hits
}

// authors refuses a commit with no linked account. Its address is never ours, because the own-work guard refuses that first,
// so the commit may still be ours under a wrong address.
func (a *attempt) authors() *refusal {
	if len(a.view.Commits) == 0 {
		return refuse("REFUSE: no commit authors to check; refusing to call that clean")
	}

	unlinked := unlinkedCommits(a.view)
	if len(unlinked) == 0 {
		return nil
	}

	head := "REFUSE: these commits have no linked account and carry an address other than " + strings.Join(a.req.OwnEmails, ", ") + ":"

	return refuse(append(append([]string{head}, unlinked...),
		"  They may be OUR commits under a wrong address. If they are, repair them.",
		"  If they are a third party's, link the address to the account, or merge by hand.")...)
}

// unlinkedCommits returns a line of `<sha9> <address> ?` for each commit whose first author has no login.
func unlinkedCommits(view model.PullRequest) []string {
	var lines []string

	for _, commit := range view.Commits {
		email, login := noLogin, noLogin

		if len(commit.Authors) > 0 {
			email, login = orUnknown(commit.Authors[0].Email), orUnknown(commit.Authors[0].Login)
		}

		if login == noLogin {
			runes := []rune(commit.Oid)
			lines = append(lines, fmt.Sprintf("  %s %s %s", string(runes[:min(len(runes), shaWidth)]), email, login))
		}
	}

	return lines
}

func orUnknown(value string) string {
	if value == "" {
		return noLogin
	}

	return value
}
