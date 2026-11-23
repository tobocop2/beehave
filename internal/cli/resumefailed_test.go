package cli_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

var failedEnd = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

func failedRig(t *testing.T) *flowRig {
	t.Helper()

	rig := newRig(t)
	rig.steering.failed = []flow.FailedRun{
		{Item: "12", ID: "item-12-r1", Ended: failedEnd},
		{Item: "13", ID: "item-13-r2", Ended: failedEnd.Add(time.Minute)},
		{Item: "14", ID: "item-14-r1", Ended: failedEnd.Add(2 * time.Minute)},
	}

	return rig
}

func TestResumeFailed_ResumesEachPickedRunThroughTheSingleResume(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"failed-runs", "resume 12", "resume 13", "resume 14"}, rig.steering.calls, "each run goes through Resume, and no other call resumes")
	assert.Equal(t, "12\titem-12-r1\t2026-11-03T09:00:00Z\tresumed\n13\titem-13-r2\t2026-11-03T09:01:00Z\tresumed\n14\titem-14-r1\t2026-11-03T09:02:00Z\tresumed\n3 resumed, 0 skipped, 0 refused\n", stdout)

	rig = failedRig(t)

	code, _, stderr = rig.execute(t.Context(), "resume", "--failed", "--on-this-build")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"failed-runs", "resume-here 12", "resume-here 13", "resume-here 14"}, rig.steering.calls)
}

func TestResumeFailed_TheFlagsFormTheFilterThatTheListGets(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)

	code, _, stderr := rig.execute(t.Context(), "resume", "--failed", "--since", "6h", "--until", "2026-11-03T10:00:00Z", "--project", "p", "--limit", "2")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []flow.FailedFilter{{Since: statusNow.Add(-6 * time.Hour), Until: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), Project: "p", Limit: 2}}, rig.steering.filters)

	rig = failedRig(t)

	code, _, stderr = rig.execute(t.Context(), "resume", "--failed", "--since", "2026-11-02T08:00:00+02:00")
	require.Equal(t, 0, code, stderr)
	assert.True(t, rig.steering.filters[0].Since.Equal(time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)), "an RFC 3339 time keeps its instant")
}

func TestResumeFailed_APartialFailurePrintsEachRunAndExitsOne(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)
	rig.steering.resumeErrs = map[string]error{"13": fmt.Errorf("%w: the claim of 13 is held\nby another run", errFake)}

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed")

	assert.Equal(t, 1, code)
	assert.Equal(t, []string{"failed-runs", "resume 12", "resume 13", "resume 14"}, rig.steering.calls, "a refusal does not stop the loop")
	assert.Equal(t, "12\titem-12-r1\t2026-11-03T09:00:00Z\tresumed\n13\titem-13-r2\t2026-11-03T09:01:00Z\trefused: fake failure: the claim of 13 is held by another run\n14\titem-14-r1\t2026-11-03T09:02:00Z\tresumed\n2 resumed, 0 skipped, 1 refused\n", stdout)
	assert.Contains(t, stderr, "1 of 3 runs refused")
}

func TestResumeFailed_ARunResumedMeanwhileIsSkipped(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)
	rig.steering.resumeErrs = map[string]error{"13": fmt.Errorf("%w: item-13-r2 is running and only a failed run resumes", flow.ErrWrongState)}

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "13\titem-13-r2\t2026-11-03T09:01:00Z\tskipped: the run is in the wrong state for this command: item-13-r2 is running")
	assert.Contains(t, stdout, "2 resumed, 1 skipped, 0 refused\n")
}

func TestResumeFailed_FlagsNeedFailedAndRefuseEmptyValues(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args []string
		want string
	}{
		"an item with --failed":     {[]string{"resume", "12", "--failed"}, "give an item, or --failed with no item"},
		"since without --failed":    {[]string{"resume", "12", "--since", "6h"}, "--since needs --failed"},
		"until without --failed":    {[]string{"resume", "12", "--until", "6h"}, "--until needs --failed"},
		"project without --failed":  {[]string{"resume", "12", "--project", "p"}, "--project needs --failed"},
		"limit without --failed":    {[]string{"resume", "12", "--limit", "1"}, "--limit needs --failed"},
		"dry run without --failed":  {[]string{"resume", "12", "--dry-run"}, "--dry-run needs --failed"},
		"an empty project":          {[]string{"resume", "--failed", "--project", ""}, "--project is empty"},
		"an empty since":            {[]string{"resume", "--failed", "--since", ""}, "--since \"\" is not a time"},
		"a since that is no time":   {[]string{"resume", "--failed", "--since", "yesterday"}, "--since \"yesterday\" is not a time"},
		"an until that is no time":  {[]string{"resume", "--failed", "--until", "soon"}, "--until \"soon\" is not a time"},
		"a negative duration":       {[]string{"resume", "--failed", "--since", "-6h"}, "is not a time"},
		"a since in the future":     {[]string{"resume", "--failed", "--since", "2026-11-04T00:00:00Z"}, "is in the future"},
		"until before since":        {[]string{"resume", "--failed", "--since", "2h", "--until", "6h"}, "--until is before --since: swap them"},
		"a negative limit":          {[]string{"resume", "--failed", "--limit", "-1"}, "--limit is -1"},
		"no item and no --failed":   {[]string{"resume"}, "give the item first"},
		"a bad item without filter": {[]string{"resume", "no way"}, "use an item id"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := failedRig(t)

			code, _, stderr := rig.execute(t.Context(), tc.args...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, tc.want)
			assert.Empty(t, rig.steering.calls, "a refused command reads and changes nothing")
		})
	}
}

func TestResumeFailed_NoMatchExitsOneAndNamesTheFilter(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed", "--since", "2026-11-01T00:00:00Z", "--until", "2026-11-02T00:00:00Z", "--project", "p")

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "no failed run has ended since 2026-11-01T00:00:00Z, ended until 2026-11-02T00:00:00Z, project p")

	code, _, stderr = rig.execute(t.Context(), "resume", "--failed")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "the store holds no failed run")
	assert.Equal(t, []string{"failed-runs", "failed-runs"}, rig.steering.calls, "no run resumes")
}

func TestResumeFailed_ADryRunPrintsTheListAndResumesNothing(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed", "--dry-run")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"failed-runs"}, rig.steering.calls)
	assert.Equal(t, "12\titem-12-r1\t2026-11-03T09:00:00Z\twould resume\n13\titem-13-r2\t2026-11-03T09:01:00Z\twould resume\n14\titem-14-r1\t2026-11-03T09:02:00Z\twould resume\n3 would resume\n", stdout)

	rig = newRig(t)

	code, _, stderr = rig.execute(t.Context(), "resume", "--failed", "--dry-run")
	assert.Equal(t, 1, code, "a dry run with no run exits 1, as the real run does")
	assert.Contains(t, stderr, "no failed run")
}

func TestResumeFailed_PrintsTheRunThatTheResumeActedOnNotTheRunOfTheList(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)
	rig.steering.resumedIDs = map[string]string{"13": "item-13-r3"}

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed")
	require.Equal(t, 0, code, stderr)

	assert.Contains(t, stdout, "13\titem-13-r3\t-\tresumed\n", "a new attempt started after the list, and the resume reopened it: it has no end time of the list to show")
	assert.NotContains(t, stdout, "item-13-r2")
	assert.Contains(t, stdout, "12\titem-12-r1\t", "the other runs print their own ids")
}

func TestResumeFailed_ARunWithAnUnreadableInputIsReportedAndTheOthersResume(t *testing.T) {
	t.Parallel()

	rig := failedRig(t)
	rig.steering.unreadable = []flow.UnreadableRun{{ID: "item-71-r1", Err: fmt.Errorf("%w: read the input of run item-71-r1", errFake)}}

	code, stdout, stderr := rig.execute(t.Context(), "resume", "--failed")

	assert.Equal(t, 1, code, "the exit code stays non-zero")
	assert.Equal(t, []string{"failed-runs", "resume 12", "resume 13", "resume 14"}, rig.steering.calls, "the readable runs resume")
	assert.Contains(t, stdout, "3 resumed, 0 skipped, 0 refused\n")
	assert.Contains(t, stderr, "item-71-r1\tnot resumed: fake failure: read the input of run item-71-r1")
	assert.Contains(t, stderr, "1 runs have an input that cannot be read")

	rig = failedRig(t)
	rig.steering.unreadable = []flow.UnreadableRun{{ID: "item-71-r1", Err: errFake}}

	code, stdout, stderr = rig.execute(t.Context(), "resume", "--failed", "--dry-run")

	assert.Equal(t, 1, code, "a dry run reports the unreadable run too")
	assert.Equal(t, []string{"failed-runs"}, rig.steering.calls)
	assert.Contains(t, stdout, "3 would resume\n")
	assert.Contains(t, stderr, "item-71-r1\tnot resumed")

	rig = newRig(t)
	rig.steering.unreadable = []flow.UnreadableRun{{ID: "item-71-r1", Err: errFake}}

	code, _, stderr = rig.execute(t.Context(), "resume", "--failed")

	assert.Equal(t, 1, code, "only unreadable runs is not an empty list")
	assert.Contains(t, stderr, "item-71-r1")
	assert.NotContains(t, stderr, "no failed run matches")
}

func TestResumeFailed_AListThatCannotBeReadIsReportedInTheRealRunAndTheDryRun(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"resume", "--failed"}, {"resume", "--failed", "--dry-run"}} {
		rig := failedRig(t)
		rig.steering.failedErr = errFake

		code, stdout, stderr := rig.execute(t.Context(), args...)

		assert.Equal(t, 1, code, args)
		assert.Contains(t, stderr, "fake failure", args)
		assert.Empty(t, stdout, "a failed read prints no run line")
		assert.Equal(t, []string{"failed-runs"}, rig.steering.calls, "nothing resumes after a failed read")
	}
}
