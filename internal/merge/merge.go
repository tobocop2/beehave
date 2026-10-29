// Package merge merges one reviewed pull request into the base, pinned to the commit that a reviewer gated.
// Every guard refuses before the merge call. A pull request of ours merges only when the request says another step approved it.
package merge

import (
	"context"
	"regexp"
	"slices"

	"github.com/tobocop2/obee/internal/model"
)

// shaLength is the length of a full commit id. The merge call pins to the gated head, and a shorter pin is refused.
const shaLength = 40

// Stop is why Run ended. The value of a Stop is its exit code, except for OwnWork.
type Stop uint8

// The closed set of stops.
const (
	Done            Stop = 0
	Refused         Stop = 2
	NoWorkflowScope Stop = 3
	OwnWork         Stop = 4
)

// Stops returns every value of Stop.
func Stops() []Stop {
	return []Stop{Done, Refused, NoWorkflowScope, OwnWork}
}

// Code returns the exit code of the stop. A value outside the set is Refused.
func (s Stop) Code() int {
	if !slices.Contains(Stops(), s) {
		return int(Refused)
	}

	return int(s)
}

func (s Stop) String() string {
	names := map[Stop]string{Done: "DONE", Refused: "REFUSED", NoWorkflowScope: "NO WORKFLOW SCOPE", OwnWork: "OWN WORK"}
	if name, ok := names[s]; ok {
		return name
	}

	return "UNKNOWN"
}

// Reads is what the guards read from the forge. TokenScopes is read only for a pull request that changes a workflow.
type Reads interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	PullCounts(ctx context.Context, pr int) (commits, files int, err error)
	Committers(ctx context.Context, pr int) ([]model.Committer, error)
	PRChecks(ctx context.Context, pr int) ([]model.Check, error)
	OpenChangeRequests(ctx context.Context, pr int) ([]model.ChangeRequest, error)
	UnresolvedThreads(ctx context.Context, pr int) ([]model.ReviewThread, error)
	StackID(ctx context.Context, pr int) (string, error)
	TokenScopes(ctx context.Context) ([]string, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
}

// Writes is what the merge changes on the forge. MergePR merges only if the head is still the pin.
type Writes interface {
	SetDraft(ctx context.Context, pr int, draft bool) error
	SetBase(ctx context.Context, pr int, base string) error
	MergePR(ctx context.Context, pr int, sha string) error
}

// Body is the body of a pull request. The merge records its row there, through EditBody.
type Body interface {
	PRBody(ctx context.Context, pr int) (string, error)
	SetBody(ctx context.Context, pr int, body string) error
}

// Forge is the forge the merge works on.
type Forge interface {
	Reads
	Writes
	Body
}

// Request is one merge. Gated is the full commit id the reviewers gated. Runners are the logins of runners.star.
// Repo and Base name the repository and the base. OwnLogins and OwnEmails are the identities of the owner; both lists need an entry, and no entry may be empty.
// Approved is true when a step other than the author accepted the head: work of an own identity merges only then.
// Hold is the label of a pull request that is never merged; empty means no label holds. ForbidText holds the patterns that no line of the body or of a commit message may match.
// Retarget moves each child pull request onto the base before the merge, where a child would otherwise block it.
// IntegrationPR is the pull request that holds the row of the merge, or 0 for none. Dir is the state folder for its lock.
// GatedTree is the tree id of the gated head. When it is set, the run reads the tree of the base tip after the merge and stops if it differs; empty asks for no proof.
// Intent is the record of a caller that wrote it before the merge. With an intent, a pull request in state MERGED counts as done
// when the intent ties the merge to this run (see Intent) and the guards that still apply to a merged pull request pass. Without one, MERGED refuses.
// The three Allow fields lift a guard on request of the owner. No field lifts the own-work rule.
type Request struct {
	Intent        *Intent
	PR            int
	Gated         string
	GatedTree     string
	Repo          string
	Base          string
	OwnLogins     []string
	OwnEmails     []string
	Approved      bool
	Hold          string
	ForbidText    []*regexp.Regexp
	Retarget      bool
	Runners       []string
	Dir           string
	IntegrationPR int
	AllowOrphans  bool
	AllowNoChecks bool
	AllowReady    bool
}

// Result is the verdict. A Done result merged the pull request. Lines are the lines to show, a refusal last.
type Result struct {
	Stop   Stop
	Reason Reason
	Lines  []string
}

// Reason is why a refusal happened, for a caller that acts on the cause and not on the text of a line.
type Reason uint8

// The closed set of reasons. Plain is a refusal that has no cause for a caller to act on.
const (
	Plain Reason = iota
	// Unsettled is a pull request whose mergeable state the forge has not computed yet. A later read can settle it.
	Unsettled
	// TreeProof is a merge whose tree proof failed: the tree of the base tip is not the gated tree, or it could not be compared.
	TreeProof
	// Transient is a refusal with a cause that can pass by itself: a failed read of the forge, checks that still run, a head or a base that moved
	// while the run prepared, or an error of the merge call. The pull request itself is not at fault, so a caller defers it and does not reject it.
	Transient
)

// refusal is a guard that said no. The lines come after the lines the guards already said.
type refusal struct {
	stop   Stop
	reason Reason
	lines  []string
}

func refuse(lines ...string) *refusal {
	return &refusal{stop: Refused, lines: lines}
}

// transient is a refusal for a cause that can pass by itself.
func transient(lines ...string) *refusal {
	return &refusal{stop: Refused, reason: Transient, lines: lines}
}

// attempt is one run: the request, the first read and the lines said so far.
type attempt struct {
	ctx   context.Context //nolint:containedctx // one run lives inside one call of Run
	forge Forge
	req   Request
	view  model.PullRequest
	files int // the forge's count of changed files, read with the first read
	// committers are the committers of the commits, read with the first read.
	committers []model.Committer
	// tip is the tip of the base at the start of the run. The run refuses if the base is somewhere else right before the merge call.
	tip string
	// kids are the children to move onto the base right before the merge call. Only a retarget fills it.
	kids  []int
	lines []string
}

func (a *attempt) say(lines ...string) {
	a.lines = append(a.lines, lines...)
}

// guards lists the guards in the order they run. The own-work guard reads the pull request, so it comes first.
func guards() []func(*attempt) *refusal {
	return []func(*attempt) *refusal{
		(*attempt).ownWork, (*attempt).settings, (*attempt).pin, (*attempt).start, (*attempt).intended, (*attempt).state, (*attempt).held, (*attempt).orphans, (*attempt).checks,
		(*attempt).wholeFileList, (*attempt).workflowScope, (*attempt).reviews, (*attempt).attribution, (*attempt).authors, (*attempt).draft,
	}
}

// Run runs every guard, then merges. A refusal stops before the merge call, and one of ours stops before any write.
func Run(ctx context.Context, forge Forge, req Request) Result {
	run := &attempt{ctx: ctx, forge: forge, req: req}

	for _, guard := range guards() {
		if stop := guard(run); stop != nil {
			return Result{Stop: stop.stop, Reason: stop.reason, Lines: append(run.lines, stop.lines...)}
		}
	}

	if stop := run.conclude(); stop != nil {
		return Result{Stop: stop.stop, Reason: stop.reason, Lines: append(run.lines, stop.lines...)}
	}

	return Result{Stop: Done, Lines: run.lines}
}

// text returns the text of a field that gh may send as null.
func text(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
