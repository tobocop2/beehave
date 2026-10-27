package merge

import (
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// listLimit is the most pull requests one list asks for. The forge refuses a list that fills it, as it may be cut off.
const listLimit = 1000

// orphans refuses a merge that would leave open pull requests with no base. A child is an open pull request on the branch of this one.
// A child in the same native stack is a note, not a block: GitHub retargets it when its parent merges.
func (a *attempt) orphans() *refusal {
	blocking, stop := a.children()
	if stop != nil || len(blocking) == 0 {
		return stop
	}

	a.say(
		fmt.Sprintf("REFUSE: #%d is the base of %s. A squash merge orphans them.", a.req.PR, strings.Join(blocking, " ")),
		fmt.Sprintf("        Link them into the stack of #%d, or rebase each child onto %s,", a.req.PR, a.req.Settings.Base()),
		"        or merge the children before the parent, or use a merge commit so their base survives.",
		"        Then run the merge again.",
		"        To merge anyway, pass --allow-orphans, and say in the verdict what happens to each child.",
	)

	if !a.req.AllowOrphans {
		return refuse()
	}

	a.say(fmt.Sprintf("        --allow-orphans is set; merging and orphaning %s is now a stated decision.", strings.Join(blocking, " ")))

	return nil
}

// children returns the children that a merge would orphan, as #N. A read that fails refuses: an unreadable guard is not a passing one.
func (a *attempt) children() ([]string, *refusal) {
	branch := text(a.view.HeadRefName)
	if branch == "" {
		return nil, refuse(fmt.Sprintf("REFUSE: cannot read #%d's own branch name, so its children cannot be found", a.req.PR))
	}

	rows, err := a.forge.PRList(a.ctx, listLimit, "--state", "open", "--base", branch, "--json", "number,baseRefName")
	if err != nil {
		return nil, refuse(err.Error(), "REFUSE: cannot read the open pull requests to check for stacked children")
	}

	if kids := onBranch(rows, branch); len(kids) > 0 {
		return a.unstacked(kids)
	}

	return nil, nil
}

// onBranch returns the numbers of the rows whose base is the branch.
func onBranch(rows []model.PullRequest, branch string) []int {
	var kids []int

	for _, row := range rows {
		if text(row.BaseRefName) == branch {
			kids = append(kids, row.Number)
		}
	}

	return kids
}

// unstacked returns the children that are not in the stack of the pull request itself, and notes the ones that are.
func (a *attempt) unstacked(kids []int) ([]string, *refusal) {
	mine, err := a.forge.StackID(a.ctx, a.req.PR)
	if err != nil {
		return nil, refuse(fmt.Sprintf("REFUSE: cannot read which stack #%d is in", a.req.PR))
	}

	var blocking []string

	for _, kid := range kids {
		same, stop := a.sameStack(mine, kid)
		if stop != nil {
			return nil, stop
		}

		if !same {
			blocking = append(blocking, fmt.Sprintf("#%d", kid))
		}
	}

	return blocking, nil
}

// sameStack tells whether a child is in the stack of the parent, and notes it when it is. A parent in no stack has no layer, so it asks nothing.
func (a *attempt) sameStack(mine string, kid int) (bool, *refusal) {
	if mine == "" {
		return false, nil
	}

	theirs, err := a.forge.StackID(a.ctx, kid)
	if err != nil {
		return false, refuse(fmt.Sprintf("REFUSE: cannot read which stack #%d is in", kid))
	}

	if theirs == mine {
		a.say(fmt.Sprintf("note: #%d is the next layer of stack %s; GitHub retargets it when #%d merges", kid, mine, a.req.PR))
	}

	return theirs == mine, nil
}
