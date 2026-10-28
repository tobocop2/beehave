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
// With Retarget set, the guard only records the children. The move happens in conclude, after every other guard.
func (a *attempt) orphans() *refusal {
	kids, stop := a.children()
	if stop != nil || len(kids) == 0 {
		return stop
	}

	if a.req.Retarget {
		a.kids = kids

		return nil
	}

	return a.block(hashed(kids))
}

// block says why the children stop the merge. It refuses unless --allow-orphans is set.
func (a *attempt) block(blocking []string) *refusal {
	a.say(
		fmt.Sprintf("REFUSE: #%d is the base of %s. A squash merge orphans them.", a.req.PR, strings.Join(blocking, " ")),
		fmt.Sprintf("        Link them into the stack of #%d, or rebase each child onto %s,", a.req.PR, a.req.Base),
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

// hashed writes each pull request number as #N.
func hashed(numbers []int) []string {
	names := make([]string, len(numbers))

	for i, number := range numbers {
		names[i] = fmt.Sprintf("#%d", number)
	}

	return names
}

// retarget moves each child onto the base, so the merge orphans none. It runs after every guard has passed, right before the merge call.
// A move that fails refuses and the merge does not happen. The children before it stay moved, and the refusal names both groups.
func (a *attempt) retarget() *refusal {
	for i, kid := range a.kids {
		if err := a.forge.SetBase(a.ctx, kid, a.req.Base); err != nil {
			left := listed(hashed(a.kids[i:]))

			return refuse(fmt.Sprintf("REFUSE: cannot move #%d onto %s: %s. Moved: %s. Not moved: %s. #%d is not merged. Move %s by hand, then run the merge again.",
				kid, a.req.Base, err, listed(hashed(a.kids[:i])), left, a.req.PR, left))
		}

		a.say(fmt.Sprintf("retargeted #%d onto %s before the merge of #%d", kid, a.req.Base, a.req.PR))
	}

	return nil
}

// listed joins names with commas. No name gives "none".
func listed(names []string) string {
	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ", ")
}

// children returns the children that a merge would orphan. A read that fails refuses: an unreadable guard is not a passing one.
func (a *attempt) children() ([]int, *refusal) {
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
func (a *attempt) unstacked(kids []int) ([]int, *refusal) {
	mine, err := a.forge.StackID(a.ctx, a.req.PR)
	if err != nil {
		return nil, refuse(fmt.Sprintf("REFUSE: cannot read which stack #%d is in", a.req.PR))
	}

	var blocking []int

	for _, kid := range kids {
		same, stop := a.sameStack(mine, kid)
		if stop != nil {
			return nil, stop
		}

		if !same {
			blocking = append(blocking, kid)
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
