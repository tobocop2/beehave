package review

// Class says why a finding is open. A blocking finding stops the head. The other classes are follow-ups: work the review leaves for later.
type Class string

// The classes of a finding.
const (
	Blocking    Class = "blocking"
	Deferred    Class = "deferred"
	PreExisting Class = "pre-existing"
	Minor       Class = "minor"
)

// Finding is one open item of a review.
type Finding struct {
	Class Class
	Text  string
}

// Policy is what the workflow allows. With NoFollowUps set, no item may stay open: every finding needs a commit.
type Policy struct {
	NoFollowUps bool
}

// NeedsCommit says whether the open findings need a commit. A blocking finding needs one. A follow-up needs one only when the policy forbids follow-ups.
func NeedsCommit(findings []Finding, policy Policy) bool {
	for _, found := range findings {
		if found.Class == Blocking || policy.NoFollowUps {
			return true
		}
	}

	return false
}
