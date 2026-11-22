package batch

import (
	"github.com/tobocop2/beehave/internal/model"
)

// statusesOf lists the statuses of a member of a green union: beehave/gate on its head, and beehave/review on its head when a review approved it.
// A status stands for the head it names, so a later push to the member leaves its new head with none.
func statusesOf(m Member, commit string) []model.CommitStatus {
	var statuses []model.CommitStatus

	if m.Approved {
		statuses = append(statuses, model.CommitStatus{SHA: m.Head, Context: ContextReview, State: model.StatusSuccess, Description: "The review approved this head."})
	}

	return append(statuses, model.CommitStatus{SHA: m.Head, Context: ContextGate, State: model.StatusSuccess, Description: "The gate passed on the union commit " + commit + "."})
}
