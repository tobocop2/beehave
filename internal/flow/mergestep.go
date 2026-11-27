package flow

import (
	"go.starlark.net/starlark"

	gatepkg "github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow"
)

// errNoMerger is returned when a merge call runs in an engine that has no merger.
var errNoMerger = &absentError{message: "this engine has no merger. Use a build that sets a merger", step: "merge the pull requests"}

// MergeSettings are the values of a merge call. A value that the call leaves out is empty.
// Logins and Emails are the identities of `own`. Approved is true when another step accepted the head.
type MergeSettings struct {
	Repo       string
	Base       string
	Hold       string
	Logins     []string
	Emails     []string
	ForbidText []string
	Approved   bool
	// OwnDraft is the number of the draft that the run opened for the member. The draft guard of the merge accepts that pull request.
	OwnDraft int
	Retarget bool
	// Closes are the issues that the pull request must close, and no others. Empty means it closes none.
	Closes []int
	// GatedTree is the tree id that the gate recorded for the head. When it is set, the merge compares the tree of the base tip with it.
	GatedTree string
	// Intent is the record that merge wrote before the merge. It lets a pull request that is already merged count as done.
	Intent *merge.Intent
	// Kept are the issues that the owner kept by an answer at the scope check. A closing keyword in a commit message may name them.
	Kept []model.IssueRef
	// Closing is the set of closing issues that the scope check read for the member. The merge refuses a pull request whose set differs. Nil pins no set.
	Closing []model.IssueRef
	// Statuses are the commit statuses that the merge posts on the head after every guard passed and right before the merge call. Empty posts none.
	Statuses []model.CommitStatus
}

// The statuses of a gate step, as the gate package names them.
const (
	passStatus = string(gatepkg.StatusPass)
	failStatus = string(gatepkg.StatusFail)
	// unknownStatus is a step that gave no result, as a voided gate leaves every step.
	unknownStatus = string(gatepkg.StatusUnknown)
)

// read checks the `forbid_text` and `own` arguments of a merge call and keeps them in s.
func (s *MergeSettings) read(forbid, own starlark.Value) (err error) {
	if s.ForbidText, err = workflow.Words("forbid_text", forbid); err != nil {
		return err
	}

	s.Logins, s.Emails, err = workflow.OwnerIdentity(own)

	return err
}
