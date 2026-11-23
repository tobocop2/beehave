package merge

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/closing"
)

// shortSHA is the length of a commit id in a line of a refusal.
const shortSHA = 9

// commitKeywords refuses a commit whose message has a closing keyword for an issue that the item does not name and the owner did not keep.
// GitHub closes such an issue when the commit reaches the default branch, and the closing references of the pull request do not list it.
// The commits come from the first read, whose count the own-work guard has already compared with the count of the forge, so a failed or cut read has refused before.
func (a *attempt) commitKeywords() *refusal {
	var lines []string

	for _, commit := range a.view.Commits {
		sha := commit.Oid[:min(len(commit.Oid), shortSHA)]

		for _, ref := range a.closedBy(commit.MessageHeadline + "\n" + commit.MessageBody) {
			lines = append(lines, fmt.Sprintf("  commit %s closes %s", sha, ref))
		}
	}

	if len(lines) == 0 {
		return nil
	}

	head := fmt.Sprintf("REFUSE: commit messages of #%d hold a closing keyword for an issue that the item does not name:", a.req.PR)

	return refuse(append([]string{head}, append(lines, "  The owner decides: remove the keyword or name the issue.")...)...)
}

// closedBy returns the references in a message that name an issue other than the issues of the item or the issues the owner kept.
// A reference of another repository is always one of them. A reference with the name of this repository counts as a plain number.
func (a *attempt) closedBy(message string) []string {
	var out []string

	for _, ref := range closing.References(message) {
		repo, digits, _ := strings.Cut(ref, "#")
		if repo != "" && !strings.EqualFold(repo, a.req.Repo) {
			out = append(out, ref)

			continue
		}

		number, err := strconv.Atoi(digits)
		if err != nil || !slices.Contains(a.req.Names, number) && !slices.Contains(a.req.Kept, number) {
			out = append(out, "#"+digits)
		}
	}

	return out
}
