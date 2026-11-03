package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The probe prompts print one fact (the owner) and nothing else of the inputs. The review and design probes keep the marker and the output line of the real prompt,
// so the fake agent answers them. The probe reads the input `facts` with `index`, so a step with no such input renders NO-FACTS-INPUT and the assertion fails;
// no template error stands in for it.
const (
	factsProbe       = "OWNER-IS={{ with index . \"facts\" }}{{ .people.owner }}{{ else }}NO-FACTS-INPUT{{ end }}\n"
	reviewProbe      = reviewMarker + "\nWrite it to {{ .output_file }} as JSON\n" + factsProbe
	designProbe      = designMarker + "\nWrite it to {{ .output_file }} as JSON\n" + factsProbe
	fixProbe         = "FIX-PROBE\n" + factsProbe
	factsProbeAnswer = "OWNER-IS=ann"
)

// Every agent step that review_fix starts (review, design and fix) has the facts in its inputs.
func TestReviewFix_EveryAgentTheLoopStartsGetsTheFactsInItsInputs(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, func(review int) string {
		if review <= 4 {
			return "changes"
		}

		return "approve"
	}, noFindings, sameMechanism, `{"decision":"keep","reason":"the retry is the smallest part"}`)
	rig.h.facts = ownedBy("ann")

	prompts := filepath.Join(filepath.Dir(file), "prompts")
	for name, probe := range map[string]string{"review.md": reviewProbe, "design.md": designProbe, "fix.md": fixProbe} {
		require.NoError(t, os.WriteFile(filepath.Join(prompts, name), []byte(probe), 0o600))
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	count := map[string]int{}

	for _, request := range rig.h.agents.requests {
		for kind, marker := range map[string]string{"review": reviewMarker, "design": designMarker, "fix": "FIX-PROBE"} {
			if strings.Contains(request.Prompt, marker) {
				count[kind]++

				assert.Contains(t, request.Prompt, factsProbeAnswer, "the %s agent has no facts in its inputs", kind)
			}
		}
	}

	assert.Positive(t, count["review"], "the loop started a review agent")
	assert.Positive(t, count["design"], "the loop started a design agent")
	assert.Positive(t, count["fix"], "the loop started a fix agent")
}
