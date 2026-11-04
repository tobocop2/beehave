package merge

import (
	"errors"
	"fmt"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

// DefaultSkew is the margin for the clock skew between beehave and the forge, used when an intent sets none.
const DefaultSkew = 2 * time.Minute

// Intent is the record that a caller writes before a merge, so that a run which repeats after a crash can tell its own merge from none.
// State is the state of the pull request when the intent was written: only an OPEN read orders the intent before any merge.
// Time is the local clock at that read. Skew is the margin between that clock and the forge clock; zero means DefaultSkew.
// A zero Skew means DefaultSkew, so a caller cannot ask for no margin at all.
// BaseTipTree is the tree of the base tip at that read. The batch merge reads it; merge.Run does not.
type Intent struct {
	PR          int
	Head        string
	Base        string
	BaseTipTree string
	State       string
	Time        time.Time
	Skew        time.Duration
}

// NewIntent makes the intent from a read of the pull request. It writes nothing to the forge.
// A read that lacks the state, the head or the base is an error, because an intent with a hole proves nothing.
func NewIntent(view model.PullRequest, baseTipTree string, now time.Time) (Intent, error) {
	state, head, base := model.Deref(view.State), model.Deref(view.HeadRefOid), model.Deref(view.BaseRefName)
	if state == "" || head == "" || base == "" {
		return Intent{}, fmt.Errorf("cannot make an intent for #%d: the read has no state, head or base", view.Number)
	}

	return Intent{PR: view.Number, Head: head, Base: base, BaseTipTree: baseTipTree, State: state, Time: now}, nil
}

var errNoMergeTime = errors.New("no readable merge time")

// replayed is true when the request has an intent and the first read found the pull request merged.
func (a *attempt) replayed() bool {
	return a.req.Intent != nil && model.Deref(a.view.State) == stateMerged
}

// asked is the list of fields of the first read. An intent adds the merge time.
func (a *attempt) asked() []string {
	if a.req.Intent == nil {
		return fields()
	}

	return append(fields(), "mergedAt")
}

// intended refuses a merged pull request that the intent does not tie to this run. The state guard checks the base and the head of the merged pull request.
// The intent must have read OPEN, which orders it before any merge. The merge time is a second check: it is not before the intent, less the skew margin.
func (a *attempt) intended() *refusal {
	if !a.replayed() {
		return nil
	}

	intent := a.req.Intent

	if intent.PR != a.req.PR || intent.Head != a.req.Gated || intent.Base != a.req.Base {
		return refuse(fmt.Sprintf("REFUSE: the intent is for #%d at %s on %s, not for #%d at %s on %s.",
			intent.PR, intent.Head, intent.Base, a.req.PR, a.req.Gated, a.req.Base))
	}

	if intent.State != stateOpen {
		return refuse(fmt.Sprintf("REFUSE: #%d is merged, and the intent did not read the pull request as OPEN, so the merge may not be ours.", a.req.PR))
	}

	return a.mergeTime()
}

// mergeTime refuses a merge that is older than the intent by more than the skew margin, and a merge with no readable time.
func (a *attempt) mergeTime() *refusal {
	intent := a.req.Intent

	mergedAt, err := parseTime(a.view.MergedAt)
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: #%d is merged and its merge time cannot be read, so the merge may not be ours.", a.req.PR))
	}

	skew := intent.Skew
	if skew == 0 {
		skew = DefaultSkew
	}

	if mergedAt.Before(intent.Time.Add(-skew)) {
		return refuse(fmt.Sprintf("REFUSE: #%d merged before the intent was written (merged %s, intent %s), so the merge is not ours.",
			a.req.PR, mergedAt.Format(time.RFC3339), intent.Time.Format(time.RFC3339)))
	}

	return nil
}

func parseTime(value *string) (time.Time, error) {
	if value == nil {
		return time.Time{}, errNoMergeTime
	}

	return time.Parse(time.RFC3339, *value)
}
