package flow

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// laneData is the data file text that each kind of agent of the example found when it started. A call with no data file records "".
type laneData struct {
	mu    sync.Mutex
	kinds []string
	texts []string
}

// kindOf names the agent call of the example that a request is: plan, review, sweep, implement or fix.
func kindOf(request AgentRequest) string {
	switch {
	case strings.Contains(request.Prompt, reviewMarker):
		return "review"
	case request.NoEdits:
		return "plan"
	case isSweep(request):
		return "sweep"
	case request.Resume != "":
		return "fix"
	}

	return "implement"
}

func (d *laneData) add(request AgentRequest) {
	text := ""
	if request.DataFile != "" {
		raw, err := os.ReadFile(filepath.Clean(request.DataFile))
		text = string(raw)

		if err != nil {
			text = "unreadable: " + err.Error()
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.kinds = append(d.kinds, kindOf(request))
	d.texts = append(d.texts, text)
}

// The first scoped review asks for changes, so the run has a plan, the implementer, a scoped review, a fix, a second scoped review and the full review.
func TestExample_TheImplementerTheFixAndTheReviewsGetTheBaseRedsInTheDataFile(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, func(review int) string {
		if review == 1 {
			return newFinding
		}

		return "[]"
	})
	round, ok := rig.project.Vars["round"].(map[string]any) // the rig builds the round settings as a dict
	require.True(t, ok)

	round["base_reds"] = []any{"lint-a", "test-b"}

	seen := &laneData{}
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		seen.add(request)

		return inner(request)
	}

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	record := `{"base":"integration","base_reds":["lint-a","test-b"]}`
	withHistory := `{"base":"integration","base_reds":["lint-a","test-b"],"pulls":[]}`

	assert.Equal(t, []string{"plan", "implement", "sweep", "review", "fix", "review", "review"}, seen.kinds)
	// The ledger of the round: the first review raises f-1, the fix and the second review see it open, and the full review sees it fixed.
	entry := func(state string) string {
		return `{"id":"f-1","last_raised":1,"path":"a.go","raised_in":1,"reason":"","severity":"blocking","state":"` + state + `","text":"the retry never stops"}`
	}
	withLedger := func(entries string) string {
		return record[:len(record)-1] + `,"ledger":[` + entries + `]}`
	}

	assert.Equal(t, []string{`{"pulls":[]}`, withHistory, "", withLedger(""), withLedger(entry("open")), withLedger(entry("open")), withLedger(entry("fixed"))}, seen.texts,
		"the plan gets the pull requests of the item and no base reds, the implementer gets both, the sweep gets no data, and each review and fix lane gets the base reds and the ledger of the round")
}

func TestExample_ThePlanAndTheImplementerGetThePullRequestsOfTheItemInTheDataFile(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.forge.history = []model.PriorPull{{Number: 4, Title: "first try", State: "CLOSED", Branch: "item-1", Base: "integration", ClosingComment: "refuted: slower"}}

	seen := &laneData{}
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		seen.add(request)

		return inner(request)
	}

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	pulls := `"pulls":[{"base":"integration","branch":"item-1","closing_comment":"refuted: slower","merged_at":"","number":4,"state":"CLOSED","title":"first try"}]`

	require.GreaterOrEqual(t, len(seen.texts), 2)
	assert.Equal(t, []string{"plan", "implement"}, seen.kinds[:2])
	assert.Equal(t, "{"+pulls+"}", seen.texts[0], "the plan reads the earlier pull requests of the item, with the closing comment")
	assert.Contains(t, seen.texts[1], pulls, "the implementer reads the same pull requests")
	assert.Len(t, rig.h.forge.historyAsked, 1, "the engine reads the pull requests once, before the plan")
}
