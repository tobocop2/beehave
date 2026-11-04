package model

import "fmt"

// Comparison is how a forge compared a commit with a branch. Status is the word of the forge: ahead, identical, behind or diverged.
// AheadBy is the number of commits the branch holds that the commit lacks. BehindBy is the number of commits the commit holds that the branch lacks.
type Comparison struct {
	Status   string
	AheadBy  int
	BehindBy int
}

// Contains is true when the branch holds the commit: the branch is ahead of the commit or equal to it, and the commit has no commit of its own.
// The word alone is not enough, so a status that says ahead with a commit behind the branch is not a landing.
func (c Comparison) Contains() bool {
	return c.BehindBy == 0 && (c.Status == "ahead" || c.Status == "identical")
}

// String prints the status with both counts, so a reader never acts on the bare word.
func (c Comparison) String() string {
	return fmt.Sprintf("%s, ahead by %d, behind by %d", c.Status, c.AheadBy, c.BehindBy)
}
