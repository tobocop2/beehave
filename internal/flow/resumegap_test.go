package flow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// gapCase is one input: the reset mode of the limit, the run of the try whose limit is the last one, and the step records the store refuses.
type gapCase struct {
	id      string
	passed  bool     // the reset time of the limit has passed when the limit step runs
	refuse  []string // the step names whose records the store refuses in the first run
	results []agent.Result
	want    string // the expected outcome of the first run, the resume and the totals
}

// gapCases generates each reset mode, each run of the try and each refusal set. A limit with no reset time is the control that no record is refused.
func gapCases() []gapCase {
	var cases []gapCase

	for _, run := range []int{0, 1} {
		for _, set := range []string{"none", "limit", "limit+spent"} {
			cases = append(cases, gapCaseOf(false, run, set))

			if run == 0 { // a second limit with a past reset fails its phase: no limit step runs
				cases = append(cases, gapCaseOf(true, run, set))
			}
		}
	}

	// The record and the failure record of the agent step of the phase child are refused: the resume continues and the agent runs again.
	// The cost of the refused run is lost with its record, as the cost of a run whose step record never committed.
	return append(cases, gapCase{
		id: "limit-noreset-refuse-none", results: []agent.Result{limited(time.Time{}, 0.7), exited},
		want: "first=failed firstcost=0.70 resume=ok agents=2 cost=0.70",
	}, gapCase{
		id: "child-agent-refused", refuse: []string{"agent"}, results: []agent.Result{limited(time.Time{}, 0.7), exited},
		want: "first=refused firstcost=0.00 resume=ok agents=2 cost=0.00",
	})
}

// gapCaseOf builds the input for a future or passed reset, the run of the try whose limit is the last one, and the refusal set: none, limit, or limit and its cost.
func gapCaseOf(passed bool, run int, set string) gapCase {
	mode, reset := "future", limitReset
	if passed {
		mode, reset = "passed", limitReset.Add(-time.Hour)
	}

	results := []agent.Result{}
	cost := 0.0

	for i := 0; i <= run; i++ {
		results = append(results, limited(reset, 0.2+0.5*float64(i)))
		cost += 0.2 + 0.5*float64(i)
	}

	refuse := map[string][]string{"none": nil, "limit": {fmt.Sprintf("limit1.%d", run)}, "limit+spent": {fmt.Sprintf("limit1.%d", run), "spent1"}}[set]
	for i := range refuse {
		refuse[i] = "agent:fix#1/" + refuse[i]
	}

	// A refused cost record loses the cost of the last run until the resume records it again.
	firstCost := cost
	if set == "limit+spent" {
		firstCost -= 0.2 + 0.5*float64(run)
	}

	want := fmt.Sprintf("first=refused firstcost=%.2f resume=ok agents=%d cost=%.2f", firstCost, run+2, cost)
	if set == "none" {
		want = fmt.Sprintf("first=ok firstcost=%.2f resume=none agents=%d cost=%.2f", cost, run+2, cost)
	}

	return gapCase{
		id: fmt.Sprintf("limit-%s-run%d-refuse-%s", mode, run, set), passed: passed,
		refuse: refuse, results: append(results, exited), want: want,
	}
}

// gapOutcome names how a run ended.
func gapOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case strings.Contains(err.Error(), "UnexpectedStep"):
		return "unexpected-step"
	case strings.Contains(err.Error(), "the store refused the record"):
		return "refused"
	}

	return "failed"
}

func TestResume_AfterTheStoreRefusedAStepAndItsFailureRecordContinuesFromTheGap(t *testing.T) {
	t.Parallel()

	for _, c := range gapCases() {
		t.Run(c.id, func(t *testing.T) {
			t.Parallel()

			wall := limitReset.Add(-30 * time.Millisecond)
			if c.passed {
				wall = limitReset
			}

			h := limitedHarness(t, wall)
			inTurn(h, c.results...)

			engine := h.engine(t)

			if len(c.refuse) > 0 {
				_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_gap BEFORE INSERT ON operation_outputs WHEN NEW.function_name IN ('"+
					strings.Join(c.refuse, "','")+"') BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
				require.NoError(t, err)
			}

			_, err := engine.Run(testItem, testProject)
			first := gapOutcome(err)

			firstCost, costErr := runAndBelowSpend(engine.store(), testRun)
			require.NoError(t, costErr)

			resume := "none"

			if err != nil {
				if len(c.refuse) > 0 {
					_, dropErr := sqlOf(t, h).ExecContext(t.Context(), "DROP TRIGGER refuse_gap")
					require.NoError(t, dropErr)
				}

				_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
				resume = gapOutcome(err)
			}

			cost, costErr := runAndBelowSpend(engine.store(), testRun)
			require.NoError(t, costErr)

			require.Equal(t, c.want, fmt.Sprintf("first=%s firstcost=%.2f resume=%s agents=%d cost=%.2f", first, firstCost.Cost, resume, len(h.agents.seen()), cost.Cost))
		})
	}
}
