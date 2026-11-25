package merge_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// alerting is the fake forge that also reads the security alerts of a pull request. asks counts the reads and heads holds the commit of each one.
type alerting struct {
	*world
	found []model.Alert
	err   error
	asks  int
	heads []string
}

func (a *alerting) OpenAlerts(_ context.Context, _ int, sha string) ([]model.Alert, error) {
	a.asks++
	a.heads = append(a.heads, sha)

	return a.found, a.err
}

func alerted(t *testing.T) *alerting {
	t.Helper()

	return &alerting{world: newWorld(t)}
}

func withAlerts() merge.Request {
	req := request()
	req.Alerts = true

	return req
}

func TestMerge_AnOpenAlertAtTheHeadBlocksTheMerge(t *testing.T) {
	t.Parallel()

	a := alerted(t)
	a.found = []model.Alert{{Number: 3, Rule: "go/sql-injection", Severity: "high", URL: "https://example.test/3"}, {Number: 4, Rule: "go/xss", Severity: "low", URL: "https://example.test/4"}}

	result := merge.Run(context.Background(), a, withAlerts())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.NotEqual(t, merge.Transient, result.Reason, "an open alert is a verdict on the pull request")
	assert.Contains(t, all(result), "#3 go/sql-injection (high)")
	assert.Contains(t, all(result), "#4 go/xss (low)")
	assert.Empty(t, a.writes, "no write before the refusal")
	assert.Equal(t, []string{sha}, a.heads, "the read asks for the gated head")
}

func TestMerge_NoOpenAlertLetsTheMergeGoOn(t *testing.T) {
	t.Parallel()

	a := alerted(t)

	result := merge.Run(context.Background(), a, withAlerts())

	assert.Equal(t, merge.Done, result.Stop, all(result))
	assert.Equal(t, 1, a.asks)
	assert.Len(t, merges(a.world), 1)
}

func TestMerge_AlertsOnAndTheReadFailsRefuses(t *testing.T) {
	t.Parallel()

	a := alerted(t)
	a.err = errors.New("the read failed")

	result := merge.Run(context.Background(), a, withAlerts())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Transient, result.Reason, "a failed read can pass by itself")
	assert.Contains(t, all(result), "could not be read")
	assert.Empty(t, a.writes)
}

func TestMerge_AlertsOnAndTheForgeCannotReadThemRefuses(t *testing.T) {
	t.Parallel()

	a := alerted(t)
	a.err = fmt.Errorf("%w: security alerts: code scanning is off", model.ErrNoCapability)

	result := merge.Run(context.Background(), a, withAlerts())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.NotEqual(t, merge.Transient, result.Reason, "a forge that cannot read alerts does not change by waiting")
	assert.Contains(t, all(result), "security alerts")
	assert.Empty(t, a.writes)

	plain := newWorld(t)
	result = merge.Run(context.Background(), plain, withAlerts())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, all(result), "security alerts", "a forge with no alert read is refused, and the merge is not made")
	assert.Empty(t, plain.writes)
}

func TestMerge_AlertsOffReadsNoAlerts(t *testing.T) {
	t.Parallel()

	a := alerted(t)
	a.found = []model.Alert{{Number: 3, Rule: "r", Severity: "high"}}

	result := merge.Run(context.Background(), a, request())

	assert.Equal(t, merge.Done, result.Stop, all(result))
	assert.Zero(t, a.asks)
}
