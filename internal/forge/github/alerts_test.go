package github_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/forge/forgetest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const alertsPath = "repos/owner/name/code-scanning/alerts?state=open&per_page=100&pr=7"

func alertRow(number int, commit, severity, security string) fields {
	return fields{
		"number":               number,
		"html_url":             "https://example.test/alerts/" + strconv.Itoa(number),
		"rule":                 fields{"id": "go/rule-" + strconv.Itoa(number), "severity": severity, "security_severity_level": security},
		"most_recent_instance": fields{"commit_sha": commit},
	}
}

func TestOpenAlerts_ReturnsTheOpenAlertsOfTheHead(t *testing.T) {
	t.Parallel()

	other := "ffffffffffffffffffffffffffffffffffffffff"
	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []any{[]fields{alertRow(1, forgetest.SevenTip, "warning", "high"), alertRow(2, other, "error", ""), alertRow(3, forgetest.SevenTip, "note", "")}}),
		"api", "--paginate", "--slurp", alertsPath)

	got, err := newForge(t, repo, run).OpenAlerts(context.Background(), 7, forgetest.SevenTip)

	require.NoError(t, err)
	assert.Equal(t, []model.Alert{
		{Number: 1, Rule: "go/rule-1", Severity: "high", URL: "https://example.test/alerts/1"},
		{Number: 3, Rule: "go/rule-3", Severity: "note", URL: "https://example.test/alerts/3"},
	}, got, "the alert at another commit is left out, and the rule severity stands in for a missing security severity")
}

func TestOpenAlerts_AnAnswerWithNoAlertAtTheHeadIsAnEmptyListAndNoError(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []any{[]fields{}}), "api", "--paginate", "--slurp", alertsPath)

	got, err := newForge(t, repo, run).OpenAlerts(context.Background(), 7, forgetest.SevenTip)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestOpenAlerts_ReadsTheAlertStateNotTheComments(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []any{[]fields{alertRow(1, forgetest.SevenTip, "error", "critical")}}), "api", "--paginate", "--slurp", alertsPath)

	got, err := newForge(t, repo, run).OpenAlerts(context.Background(), 7, forgetest.SevenTip)

	require.NoError(t, err)
	require.Len(t, got, 1, "a bot comment that says no alerts does not clear an open alert")
	assert.Equal(t, 1, got[0].Number)
	assert.Equal(t, [][]string{{"api", "--paginate", "--slurp", alertsPath}}, run.Calls, "no comment is read")
}

func TestOpenAlerts_AFailedReadIsAnError(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "boom")
	forgetest.CheckAlertsFail(t, down)

	garbled, _ := fixed(t, "not json")
	_, err := garbled.OpenAlerts(context.Background(), 7, forgetest.SevenTip)
	require.Error(t, err, "an answer of the wrong shape is an error and no empty list")
}

func TestProbe_CodeScanningOffIsNoCapability(t *testing.T) {
	t.Parallel()

	for _, stderr := range []string{"gh: Not Found (HTTP 404)", "gh: Resource not accessible by integration (HTTP 403)"} {
		f, run := failing(t, stderr)

		err := f.Probe(context.Background(), forge.AbilityAlerts)

		require.ErrorIs(t, err, forge.ErrNoCapability, stderr)
		require.ErrorContains(t, err, "security alerts")
		assert.Equal(t, [][]string{{"api", "repos/owner/name/code-scanning/alerts?per_page=1"}}, run.Calls)
	}
}

func TestProbe_CodeScanningOnIsNoError(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "[]")

	require.NoError(t, f.Probe(context.Background(), forge.AbilityAlerts))
}

func TestProbe_AFailureThatIsNoMissingAbilityIsAnErrorOfItsOwn(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "gh: HTTP 500")

	err := f.Probe(context.Background(), forge.AbilityAlerts)

	require.Error(t, err)
	assert.NotErrorIs(t, err, forge.ErrNoCapability, "a server fault says nothing about the ability")
}

func TestProbe_AnAbilityThatNeedsNoProbeMakesNoCall(t *testing.T) {
	t.Parallel()

	f, run := failing(t, "boom")

	require.NoError(t, f.Probe(context.Background(), forge.AbilityStatuses))
	assert.Empty(t, run.Calls)
}
