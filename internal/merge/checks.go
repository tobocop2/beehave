package merge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// workflowDir is the folder of the workflow files. A change there needs the workflow scope on the token.
const workflowDir = ".github/workflows/"

// checks refuses a head whose checks are not a pass. A skipped check is not a pass. Zero passes refuse unless the owner allowed it.
func (a *attempt) checks() *refusal {
	rows, err := a.forge.PRChecks(a.ctx, a.req.PR)
	if err != nil {
		return refuse(err.Error(), fmt.Sprintf("REFUSE: cannot read the checks for #%d", a.req.PR))
	}

	lines, stop := a.judge(rows)
	if stop != nil {
		stop.lines = append(stop.lines, fmt.Sprintf("       (above refers to #%d)", a.req.PR))

		return stop
	}

	a.say(lines...)

	return nil
}

// buckets holds the names of the checks by what gh judged: the ones that are not green, the ones still running and the ones that skipped.
type buckets struct {
	bad, running, skipped []string
	green                 int
}

func tally(rows []model.Check) buckets {
	var found buckets

	for _, row := range rows {
		switch row.Bucket {
		case model.CheckPass:
			found.green++
		case model.CheckPending:
			found.running = append(found.running, row.Name)
		case model.CheckSkipping:
			found.skipped = append(found.skipped, row.Name)
		default:
			found.bad = append(found.bad, row.Name+"="+string(row.Bucket))
		}
	}

	return found
}

// judge applies the rules of the checks. It returns the lines of a pass, or the refusal.
func (a *attempt) judge(rows []model.Check) ([]string, *refusal) {
	found := tally(rows)

	switch {
	case len(found.bad) > 0:
		return nil, refuse("REFUSE: checks are not green: " + strings.Join(found.bad, ","))
	case len(found.running) > 0:
		return nil, refuse(fmt.Sprintf("REFUSE: checks still running: %s. Wait for them.", strings.Join(found.running, ",")))
	case found.green == 0 && !a.req.AllowNoChecks:
		return nil, refuse(
			fmt.Sprintf("REFUSE: no check passed (%d reported, 0 passed). CI did not run on this head.", len(rows)),
			"        If every workflow skipped for a legitimate reason, run the merge again with --allow-no-checks",
			"        and state that reason in the verdict.",
		)
	}

	return found.summary(len(rows)), nil
}

// summary says what was skipped, then how many of the checks passed.
func (b buckets) summary(total int) []string {
	var lines []string
	if len(b.skipped) > 0 {
		lines = append(lines, "checks: skipped and not counted as a pass: "+strings.Join(b.skipped, ","))
	}

	return append(lines, fmt.Sprintf("checks: %d of %d passed", b.green, total))
}

// workflowScope refuses a change to a workflow file when the token has no workflow scope. It reads the scopes only then.
func (a *attempt) workflowScope() *refusal {
	if !touchesWorkflow(a.view) {
		return nil
	}

	scopes, err := a.forge.TokenScopes(a.ctx)
	if err != nil {
		return refuse("REFUSE: cannot read the token scopes, so a workflow change cannot be merged")
	}

	if slices.Contains(scopes, "workflow") {
		return nil
	}

	return &refusal{stop: NoWorkflowScope, lines: []string{
		fmt.Sprintf("REFUSE: #%d touches .github/workflows and the token has no workflow scope.", a.req.PR),
		"        Run: gh auth refresh -h github.com -s workflow",
	}}
}

// wholeFileList is the guard before the scope guard. It refuses a list of changed files that is absent or shorter than the forge's count, because a workflow file could hide in the rest.
func (a *attempt) wholeFileList() *refusal {
	if a.view.Files == nil {
		return refuse(fmt.Sprintf("REFUSE: the changed files cannot be read for #%d, so a workflow change cannot be ruled out.", a.req.PR))
	}

	if a.files > len(a.view.Files) {
		return refuse(fmt.Sprintf("REFUSE: the list of changed files is incomplete for #%d (%d read, %d in the forge), so a workflow change cannot be ruled out.",
			a.req.PR, len(a.view.Files), a.files))
	}

	return nil
}

func touchesWorkflow(view model.PullRequest) bool {
	for _, file := range view.Files {
		if strings.HasPrefix(file.Path, workflowDir) {
			return true
		}
	}

	return false
}

// reviews refuses a standing change request and an unresolved thread. An approval or a dismissal lifts a request; a comment does not.
func (a *attempt) reviews() *refusal {
	if stop := a.requests(); stop != nil {
		return stop
	}

	open, err := a.forge.UnresolvedThreads(a.ctx, a.req.PR)
	if err != nil {
		return refuse(err.Error(), fmt.Sprintf("REFUSE: the review threads of #%d could not all be read, so an unresolved one cannot be ruled out.", a.req.PR))
	}

	if len(open) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf("REFUSE: unresolved review threads on #%d (address each, then resolve the thread):", a.req.PR)}
	for _, thread := range open {
		lines = append(lines, thread.Summary())
	}

	return refuse(lines...)
}

func (a *attempt) requests() *refusal {
	standing, err := a.forge.OpenChangeRequests(a.ctx, a.req.PR)
	if err != nil {
		return refuse(err.Error(), fmt.Sprintf("REFUSE: the reviews of #%d could not be read, so a standing change request cannot be ruled out.", a.req.PR))
	}

	if len(standing) == 0 {
		return nil
	}

	return refuse(fmt.Sprintf("REFUSE: changes requested and still standing (an approval or a dismissal lifts one, a comment does not): %s. Route the review to a fix lane.",
		model.JoinChangeRequests(standing)))
}
