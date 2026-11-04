package batch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/model"
)

type issueWorld struct {
	states  map[int]string
	failing map[int]bool
	refs    []int
	refsErr error
	asked   []int
}

func (w *issueWorld) IssueState(_ context.Context, n int) (string, error) {
	w.asked = append(w.asked, n)

	if w.failing[n] {
		return "", errors.New("the forge is down")
	}

	return w.states[n], nil
}

func (w *issueWorld) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{Issues: w.refs}, w.refsErr
}

func TestAudit_ClosedIssuesAndAClosingListThatMembersClaimGiveNoFinding(t *testing.T) {
	t.Parallel()

	w := &issueWorld{states: map[int]string{4: "CLOSED", 9: "CLOSED"}, refs: []int{9, 4}}
	got := batch.Audit(t.Context(), w, []string{"4", "9"}, 70)

	assert.Empty(t, got.Findings)
	assert.NotNil(t, got.Findings)
}

func TestAudit_AnIssueThatIsStillOpenIsAFindingThatNamesTheIssueAndTheItem(t *testing.T) {
	t.Parallel()

	w := &issueWorld{states: map[int]string{4: "CLOSED", 9: "OPEN"}}
	got := batch.Audit(t.Context(), w, []string{"4", "9"}, 0)

	assert.Equal(t, []string{`issue #9 of item 9 is not closed after the batch landed (the forge says "OPEN")`}, got.Findings)
}

func TestAudit_AnIssueTheIntegrationPullRequestClosesAndNoItemNamesIsAFinding(t *testing.T) {
	t.Parallel()

	w := &issueWorld{states: map[int]string{4: "CLOSED"}, refs: []int{11, 4, 8}}
	got := batch.Audit(t.Context(), w, []string{"4"}, 70)

	assert.Equal(t, []string{
		"pull request #70 closes issue #8 and no landed item names it",
		"pull request #70 closes issue #11 and no landed item names it",
	}, got.Findings)
}

func TestAudit_AReadThatFailsIsAFindingAndNeverAPass(t *testing.T) {
	t.Parallel()

	w := &issueWorld{failing: map[int]bool{4: true}, refsErr: errors.New("no answer")}
	got := batch.Audit(t.Context(), w, []string{"4", "9"}, 70)

	assert.Equal(t, []string{
		"cannot read issue #4: the forge is down",
		`issue #9 of item 9 is not closed after the batch landed (the forge says "")`,
		"cannot read the closing references of pull request #70: no answer",
	}, got.Findings)
}

func TestAudit_AnIdThatIsNotAPlainIssueNumberIsAFindingAndReadsNothing(t *testing.T) {
	t.Parallel()

	w := &issueWorld{}
	got := batch.Audit(t.Context(), w, []string{"bb-1", "012", "0"}, 0)

	assert.Len(t, got.Findings, 3)
	assert.Contains(t, got.Findings[0], `the id "bb-1" of a landed item is not a plain issue number`)
	assert.Empty(t, w.asked)
}
