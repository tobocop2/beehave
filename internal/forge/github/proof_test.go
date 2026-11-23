package github_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/batch"
)

const (
	proofHead  = "1111111111111111111111111111111111111111"
	proofStart = "2222222222222222222222222222222222222222"
	proofTree  = "3333333333333333333333333333333333333333"
	proofEnd   = "4444444444444444444444444444444444444444"
)

// proofForge builds a forge that answers like a forge for one member whose head is not in the base. Its start tree and the tree of the base hold
// deploy.sh with startMode and endMode, and the tree of the member holds it as an executable. All three hold the same content id.
func proofForge(t *testing.T, startMode, endMode string) batch.ProofReads {
	t.Helper()

	entry := func(mode string) string {
		return `{"truncated":false,"tree":[{"path":"deploy.sh","mode":"` + mode + `","type":"blob","sha":"b1"}]}`
	}

	f, run := fixed(t, "")
	run.PlanContaining(entry(startMode), "git/trees/"+proofStart)
	run.PlanContaining(entry("100755"), "git/trees/"+proofTree)
	run.PlanContaining(entry(endMode), "git/trees/"+proofEnd)
	run.PlanContaining(proofTree, "git/commits/"+proofHead)
	run.PlanContaining(`{"object":{"sha":"`+proofHead+`"}}`, "git/ref/heads/a")
	run.PlanContaining("diverged 1 1", "compare/"+proofHead)

	return f
}

func TestProve_TheListOfTheRealReaderShowsAMemberThatOnlyMadeAFileExecutable(t *testing.T) {
	t.Parallel()

	start := batch.Tip{Commit: "start", Tree: proofStart}
	end := batch.Tip{Commit: "end", Tree: proofEnd}
	members := []batch.Member{{Item: "a", Head: proofHead}}

	missing := batch.Prove(t.Context(), proofForge(t, "100644", "100644"), "main", start, end, members)

	require.Len(t, missing, 1)
	assert.Contains(t, missing[0].Reason, "deploy.sh", "the base does not hold the executable bit, and the reason names the file")

	landed := batch.Prove(t.Context(), proofForge(t, "100644", "100755"), "main", start, end, members)

	assert.Empty(t, landed, "the base holds the executable bit, so the member is on the vehicle")
}
