package flow

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// candidateFlow runs the review loop of the library on the base main, with two candidates at most.
const candidateFlow = "load(\"@beehave//review_fix.star\", \"review_fix\")\n\n" +
	"def main(item):\n" +
	"    impl = agent(name = \"implement\", prompt = \"p.md\", model = \"sonnet\")\n" +
	"    loop = review_fix(item, impl, \"p.md\", \"p.md\", base = \"main\", rounds = 2, steps = " + loopSteps + ")\n" +
	"    return {\"approved\": loop.review != None and loop.review.verdict == \"approve\"}\n"

// loopEvent is one gate, agent or before-review command step of a dry-run line: the kind and the label.
var loopEvent = regexp.MustCompile(`^step \d+ (gate|command|agent):((?:candidate-before-review-)?[a-z0-9-]+)#\d+(/read1)?: `)

// loopEvents dry-runs the loop and returns its steps as "gate <label>", "command <label>" and "agent <name>", one for each step.
func loopEvents(t *testing.T, options ...DryOption) []string {
	t.Helper()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFileWithPrompt(t, candidateFlow), "12", nil, &out, options...), out.String())

	var events []string

	for _, line := range strings.Split(out.String(), "\n") {
		match := loopEvent.FindStringSubmatch(line)
		if match == nil || (match[1] == "agent" && match[3] == "") {
			continue
		}

		events = append(events, match[1]+" "+match[2])
	}

	return events
}

func TestLaneLayout_TheInnerLoopIsScopedAndTheFullGateRunsOncePerCandidate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		options []DryOption
		fixture string
		want    []string
	}{
		"every_review_approves": {nil, "", []string{
			"agent implement", "agent scoped-review-implement",
			"gate candidate-gate", "command candidate-before-review-ancestor", "command candidate-before-review-keywords", "agent candidate-review",
		}},
		"the_first_review_sends_back": {nil, `{
  "agent:implement": {}, "agent:scoped-review-implement": [{"outputs": {"verdict": "changes"}}], "agent:fix-scoped-2": {}, "agent:scoped-review-2": {}, "agent:candidate-review": {}
}`, []string{
			"agent implement", "agent scoped-review-implement", "agent fix-scoped-2", "agent scoped-review-2",
			"gate candidate-gate", "command candidate-before-review-ancestor", "command candidate-before-review-keywords", "agent candidate-review",
		}},
		"the_first_gate_is_red": {[]DryOption{WithRedGate()}, "", []string{
			"agent implement", "agent scoped-review-implement", "gate candidate-gate",
			"agent fix-scoped-1", "agent scoped-review-1",
			"gate candidate-gate", "command candidate-before-review-ancestor", "command candidate-before-review-keywords", "agent candidate-review",
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			options := tc.options
			if tc.fixture != "" {
				options = append(options, WithFixture(fixtureOf(t, tc.fixture)))
			}

			events := loopEvents(t, options...)

			assert.Equal(t, tc.want, events, "scoped fixes and scoped reviews run with no gate; each candidate gets one gate, the checks before review and one full review")
		})
	}
}
