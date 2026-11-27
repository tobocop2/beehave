package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
)

// ledgerEntry is one entry of the ledger, as the data file and the result field ledger hold it.
type ledgerEntry struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Severity   string `json:"severity"`
	Text       string `json:"text"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	RaisedIn   int    `json:"raised_in"`
	LastRaised int    `json:"last_raised"`
}

// ledgerScript scripts the agents of a loop. review gets the number of the review call from 0 and the ledger in its data file,
// and returns the output findings. An empty list ends the scoped reviews with an approve. fix gets the number of the fix call from 1
// and returns the outputs resolved and wontfix.
type ledgerScript struct {
	review func(n int, seen []ledgerEntry) string
	fix    func(n int, seen []ledgerEntry) (resolved, wontfix string)
}

// ledgerRun is what one run of the loop over the scripted agents left behind.
type ledgerRun struct {
	err        error
	ledger     []ledgerEntry
	reviews    []AgentRequest
	fixes      []AgentRequest
	reviewSaw  [][]ledgerEntry
	fixSaw     [][]ledgerEntry
	reviewData []string
}

func readLedger(file string) ([]ledgerEntry, string) {
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the data file of a fake agent request
	if err != nil {
		return nil, ""
	}

	var data struct {
		Ledger []ledgerEntry `json:"ledger"`
	}

	if json.Unmarshal(raw, &data) != nil {
		return nil, string(raw)
	}

	return data.Ledger, string(raw)
}

func ledgerFinding(id, path, text string) string {
	return jsonText(map[string]string{"id": id, "severity": "high", "path": path, "text": text})
}

// jsonText encodes a map of texts. A map of texts always encodes, so the error text only shows a defect of the test.
func jsonText(values map[string]string) string {
	body, err := json.Marshal(values)
	if err != nil {
		return err.Error()
	}

	return string(body)
}

func ledgerList(findings ...string) string { return "[" + strings.Join(findings, ",") + "]" }

func noFix(int, []ledgerEntry) (string, string) { return "[]", "[]" }

func byID(entries []ledgerEntry, id string) ledgerEntry {
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}

	return ledgerEntry{}
}

// runLedgerLoop runs the review loop of the library for one candidate over the scripted agents. extra is more arguments of review_fix.
func runLedgerLoop(t *testing.T, script ledgerScript, extra string) ledgerRun {
	t.Helper()

	rig := newAgentRig(t)
	rig.project = exampleProject()
	rig.h.facts = func(string) model.Project { return rig.project }
	rig.h.reader = &fakeReader{}
	rig.h.preflight = &fakePreflight{}
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head, Tree: exampleTree}}
	rig.h.gate.checks = cleanChecks()
	rig.baseAt(t, "main")

	var (
		mu  sync.Mutex
		out ledgerRun
	)

	rig.h.agents.result = func(req AgentRequest) agent.Result {
		mu.Lock()
		defer mu.Unlock()

		seen, text := readLedger(req.DataFile)

		switch {
		case strings.Contains(req.Prompt, "REVIEW"):
			findings := script.review(len(out.reviews), seen)
			verdict := "changes"

			if findings == "[]" {
				verdict = "approve"
			}

			rig.put(req.OutputFile, jsonText(map[string]string{"verdict": verdict, "findings": findings, "open": "", "mechanism": ""}))

			out.reviews, out.reviewSaw, out.reviewData = append(out.reviews, req), append(out.reviewSaw, seen), append(out.reviewData, text)
		case strings.Contains(req.Prompt, "FIX"):
			resolved, wontfix := script.fix(len(out.fixes)+1, seen)
			rig.put(req.OutputFile, jsonText(map[string]string{"resolved": resolved, "wontfix": wontfix}))

			out.fixes, out.fixSaw = append(out.fixes, req), append(out.fixSaw, seen)
		}

		return exited
	}

	flow := "load(\"@beehest//review_fix.star\", \"review_fix\")\n\n" +
		"def main(item):\n    return pipeline(item)\n\n" +
		"def pipeline(item):\n" +
		"    impl = agent(name = \"implement\", prompt = \"prompts/impl.md\", model = \"sonnet\")\n" +
		"    loop = review_fix(item, impl, \"prompts/review.md\", \"prompts/fix.md\", base = \"main\", rounds = 1, steps = " + loopSteps + extra + ")\n" +
		"    return {\"ledger\": json.encode(loop.ledger) if hasattr(loop, \"ledger\") else \"null\"}\n"

	file := writeFlow(t, flow)
	prompts := filepath.Join(filepath.Dir(file), "prompts")
	require.NoError(t, os.MkdirAll(prompts, 0o750))

	for name, text := range map[string]string{"impl.md": "IMPL\n", "review.md": "REVIEW\nWrite to {{ .output_file }}\n", "fix.md": "FIX\nWrite to {{ .output_file }}\n", "design.md": "DESIGN\nWrite to {{ .output_file }}\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(prompts, name), []byte(text), 0o600))
	}

	result, err := runPipelineOf(rig.h.engine(t), testItem, file)
	if err != nil {
		out.err = err

		return out
	}

	var value struct {
		Ledger string `json:"ledger"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))

	require.NoError(t, json.Unmarshal([]byte(value.Ledger), &out.ledger))

	return out
}

// ledgerExtra is the extra arguments of the loop that a ledger test passes: none, because the loop always keeps the ledger.
const ledgerExtra = ""

// raisedOnce scripts a first review that raises one finding and later reviews that call first(n, seen). It then approves.
func raisedOnce(first string, later func(n int, seen []ledgerEntry) string) ledgerScript {
	return ledgerScript{
		review: func(n int, seen []ledgerEntry) string {
			if n == 0 {
				return first
			}

			if later == nil {
				return "[]"
			}

			return later(n, seen)
		},
		fix: noFix,
	}
}

func TestReviewFix_ALedgerKeepsTheIdOfAFindingThatReturnsOnAMovedLine(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "a.go:10 nil deref")), func(n int, seen []ledgerEntry) string {
		if n == 1 {
			return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a.go:40 nil deref"))
		}

		return "[]"
	}), ledgerExtra)

	require.NoError(t, run.err)
	require.Len(t, run.ledger, 1)
	assert.Equal(t, "f-1", run.ledger[0].ID)
	assert.Equal(t, "a.go:40 nil deref", byID(run.reviewSaw[2], "f-1").Text, "the review after the move sees the new line under the old id")
	assert.Equal(t, 2, run.ledger[0].LastRaised)
}

func TestReviewFix_ALedgerKeepsTheIdOfAFindingThatReturnsInNewWords(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "nil deref near x")), func(n int, seen []ledgerEntry) string {
		if n == 1 {
			return ledgerList(ledgerFinding(seen[0].ID, "b.go", "missing guard around x"))
		}

		return "[]"
	}), ledgerExtra)

	require.NoError(t, run.err)
	require.Len(t, run.ledger, 1, "a reworded finding in a renamed file is one entry")
	assert.Equal(t, "f-1", run.ledger[0].ID)
	assert.Equal(t, "missing guard around x", run.ledger[0].Text)
	assert.Equal(t, "b.go", run.ledger[0].Path)
}

func TestReviewFix_ALedgerReportsAFindingThatTheNextReviewOmitsAsFixed(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), func(int, []ledgerEntry) string { return "[]" }), ledgerExtra)

	require.NoError(t, run.err)
	require.Len(t, run.ledger, 1)
	assert.Equal(t, "fixed", run.ledger[0].State)
	assert.Equal(t, 1, run.ledger[0].RaisedIn)
	assert.Equal(t, 1, run.ledger[0].LastRaised)
}

func TestReviewFix_ALedgerGivesANewFindingAnIdNoEarlierFindingHas(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), func(n int, seen []ledgerEntry) string {
		switch n {
		case 1:
			return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a"), ledgerFinding("", "b.go", "b"))
		case 2:
			return ledgerList(ledgerFinding("", "c.go", "c"))
		case 3:
			return ledgerList(ledgerFinding("", "d.go", "d"))
		}

		return "[]"
	}), ledgerExtra)

	require.NoError(t, run.err)
	require.Len(t, run.ledger, 4, "the full review adds its new finding to the ledger of the result")
	assert.Equal(t, []string{"f-1", "f-2", "f-3", "f-4"}, []string{run.ledger[0].ID, run.ledger[1].ID, run.ledger[2].ID, run.ledger[3].ID})
	assert.Equal(t, 2, run.ledger[1].RaisedIn)
	assert.Equal(t, 3, run.ledger[2].RaisedIn)
	assert.Equal(t, 4, run.ledger[3].RaisedIn)
}

func TestReviewFix_ALedgerKeepsTheIdOfAFixedFindingThatComesBack(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), func(n int, seen []ledgerEntry) string {
		switch n {
		case 1:
			return ledgerList(ledgerFinding("", "b.go", "b"))
		case 2:
			return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a again"))
		}

		return "[]"
	}), ledgerExtra)

	require.NoError(t, run.err)
	require.Len(t, run.reviewSaw, 4)
	assert.Equal(t, "fixed", byID(run.reviewSaw[2], "f-1").State, "the review that follows the omission sees the finding as fixed")
	assert.Equal(t, "open", byID(run.reviewSaw[3], "f-1").State, "the finding comes back under its first id")
	assert.Len(t, run.reviewSaw[3], 2, "the finding that came back made no new entry")
}

func TestReviewFix_ALedgerKeepsAWontfixWithItsReasonUntilAReviewRaisesItAgain(t *testing.T) {
	t.Parallel()

	wontfix := func(n int, seen []ledgerEntry) (string, string) {
		if n > 1 {
			return "[]", "[]"
		}

		return "[]", `[{"id":"` + seen[0].ID + `","reason":"the cost is not worth it"}]`
	}

	omitted := runLedgerLoop(t, ledgerScript{
		review: func(n int, _ []ledgerEntry) string {
			if n == 0 {
				return ledgerList(ledgerFinding("", "a.go", "a"))
			}

			return "[]"
		},
		fix: wontfix,
	}, ledgerExtra)

	require.NoError(t, omitted.err)
	require.Len(t, omitted.ledger, 1)
	assert.Equal(t, "wontfix", omitted.ledger[0].State, "a review that omits the finding accepts the wontfix")
	assert.Equal(t, "the cost is not worth it", omitted.ledger[0].Reason)

	raised := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), func(n int, seen []ledgerEntry) string {
			if n == 1 {
				return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a"))
			}

			return "[]"
		}).review,
		fix: wontfix,
	}, ledgerExtra)

	require.NoError(t, raised.err)
	assert.Equal(t, "wontfix", byID(raised.reviewSaw[1], "f-1").State, "the review sees the wontfix of the fix")

	reopened := byID(raised.reviewSaw[2], "f-1")
	assert.Equal(t, "open", reopened.State, "a review that lists the id again reopens it")
	assert.Equal(t, "the cost is not worth it", reopened.Reason, "the reason stays")
}

func TestReviewFix_AReviewGetsEveryEarlierFindingInItsDataFileAndAFixGetsTheOpenOnes(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, ledgerScript{
		review: func(n int, seen []ledgerEntry) string {
			switch n {
			case 0:
				return ledgerList(ledgerFinding("", "a.go", "a"), ledgerFinding("", "b.go", "b"))
			case 1:
				return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a"))
			}

			return "[]"
		},
		fix: func(n int, seen []ledgerEntry) (string, string) {
			if n == 1 {
				return "[]", `[{"id":"` + seen[1].ID + `","reason":"later"}]`
			}

			return "[]", "[]"
		},
	}, ledgerExtra)

	require.NoError(t, run.err)
	assert.Empty(t, run.reviewSaw[0], "the first review sees an empty ledger")
	assert.Len(t, run.fixSaw[0], 2)
	assert.Equal(t, "wontfix", byID(run.reviewSaw[1], "f-2").State, "a review sees the entry that is not open")
	assert.Len(t, run.fixSaw[1], 1, "a fix sees the open entries only")
	assert.Equal(t, "open", run.fixSaw[1][0].State)
	assert.Len(t, run.reviewSaw[3], 2, "the full review sees every entry")
}

func TestReviewFix_ALedgerStopsTheItemOnAnIdItDoesNotHold(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("zz-9", "a.go", "a")), nil), ledgerExtra)

	require.Error(t, run.err)
	assert.Contains(t, run.err.Error(), "REFUSE")
	assert.Contains(t, run.err.Error(), "zz-9")

	fix := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), nil).review,
		fix:    func(int, []ledgerEntry) (string, string) { return `["zz-8"]`, "[]" },
	}, ledgerExtra)

	require.Error(t, fix.err)
	assert.Contains(t, fix.err.Error(), "zz-8", "an id in the output resolved is checked")

	wontfix := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), nil).review,
		fix:    func(int, []ledgerEntry) (string, string) { return "[]", `[{"id":"zz-7","reason":"r"}]` },
	}, ledgerExtra)

	require.Error(t, wontfix.err)
	assert.Contains(t, wontfix.err.Error(), "zz-7", "an id in the output wontfix is checked")
}

func TestReviewFix_ALedgerStopsTheItemOnAFindingsOutputThatIsNotAJSONList(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"not json", `{"id":""}`, "null"} {
		run := runLedgerLoop(t, raisedOnce(text, nil), ledgerExtra)

		require.Error(t, run.err, text)
		assert.Contains(t, run.err.Error(), "REFUSE", text)
		assert.Contains(t, run.err.Error(), "findings", text)
	}

	for _, entry := range []string{`"a finding"`, `{"id":"","severity":"high","path":"a.go"}`, `{"id":1,"severity":"high","path":"a.go","text":"a"}`} {
		run := runLedgerLoop(t, raisedOnce("["+entry+"]", nil), ledgerExtra)

		require.Error(t, run.err, entry)
		assert.Contains(t, run.err.Error(), "REFUSE", entry)
		assert.Contains(t, run.err.Error(), "findings", entry)
	}

	fix := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), nil).review,
		fix:    func(int, []ledgerEntry) (string, string) { return "not json", "[]" },
	}, ledgerExtra)

	require.Error(t, fix.err)
	assert.Contains(t, fix.err.Error(), "resolved")

	shape := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), nil).review,
		fix:    func(int, []ledgerEntry) (string, string) { return "[]", `["f-1"]` },
	}, ledgerExtra)

	require.Error(t, shape.err)
	assert.Contains(t, shape.err.Error(), "wontfix")
}

func TestReviewFix_ALedgerStopsTheItemOnARepeatedId(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), func(_ int, seen []ledgerEntry) string {
		return ledgerList(ledgerFinding(seen[0].ID, "a.go", "a"), ledgerFinding(seen[0].ID, "a.go", "a"))
	}), ledgerExtra)

	require.Error(t, run.err)
	assert.Contains(t, run.err.Error(), "REFUSE")
	assert.Contains(t, run.err.Error(), "f-1")
	assert.Contains(t, run.err.Error(), "twice")
}

func TestReviewFix_ALedgerStopsTheItemOnAWontfixWithNoReason(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, ledgerScript{
		review: raisedOnce(ledgerList(ledgerFinding("", "a.go", "a")), nil).review,
		fix: func(_ int, seen []ledgerEntry) (string, string) {
			return "[]", `[{"id":"` + seen[0].ID + `","reason":"  "}]`
		},
	}, ledgerExtra)

	require.Error(t, run.err)
	assert.Contains(t, run.err.Error(), "REFUSE")
	assert.Contains(t, run.err.Error(), "reason")
}

func TestReviewFix_AnArgumentNamedLedgerIsRefusedAsUnknown(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{", ledger = True", ", ledger = False"} {
		run := runLedgerLoop(t, raisedOnce("[]", nil), extra)

		require.Error(t, run.err, extra)
		assert.Contains(t, run.err.Error(), "ledger", extra)
		assert.Contains(t, run.err.Error(), "unexpected keyword argument", extra)
	}
}

func TestReviewFix_ALedgerStopsTheItemOnAnOutputListThatNamesAnOutputOfTheLedger(t *testing.T) {
	t.Parallel()

	script := raisedOnce("[]", nil)

	for extra, word := range map[string]string{
		", review_outputs = [\"findings\"]":        "findings",
		", fix_outputs = [\"resolved\"]":           "resolved",
		", fix_outputs = [\"wontfix\", \"other\"]": "wontfix",
	} {
		run := runLedgerLoop(t, script, extra)

		require.Error(t, run.err, extra)
		assert.Contains(t, run.err.Error(), "REFUSE", extra)
		assert.Contains(t, run.err.Error(), word, extra)
	}
}

func TestReviewFix_ALedgerKeepsTheDataOfTheCallerAndTheOutputsOfThePolicies(t *testing.T) {
	t.Parallel()

	script := ledgerScript{
		review: func(n int, _ []ledgerEntry) string {
			if n == 0 {
				return ledgerList(ledgerFinding("", "a.go", "a"))
			}

			return "[]"
		},
		fix: noFix,
	}

	forbid := runLedgerLoop(t, script, ledgerExtra+`, follow_ups = "forbid"`)

	require.NoError(t, forbid.err)
	assert.Equal(t, []string{"verdict", "open", "findings"}, forbid.reviews[0].Outputs)
	assert.ElementsMatch(t, []string{"commit_subject", "commit_body", "resolved", "wontfix"}, forbid.fixes[0].Outputs)

	data := runLedgerLoop(t, script, ledgerExtra+`, data = {"x": 1}`)

	require.NoError(t, data.err)
	assert.Contains(t, data.reviewData[0], `"x"`, "the data of the caller stays beside the ledger")
	assert.Contains(t, data.reviewData[0], `"ledger"`)
}

func TestReviewFix_ALedgerOfACleanImplementerIsEmptyAndItsFirstReviewGetsAnEmptyLedger(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce("[]", nil), ledgerExtra)

	require.NoError(t, run.err)
	assert.Empty(t, run.ledger)
	require.NotEmpty(t, run.reviewData)
	assert.Contains(t, run.reviewData[0], `"ledger"`, "the first review reads an empty ledger from its data file")
	assert.Empty(t, run.reviewSaw[0])
	assert.Empty(t, run.fixes, "a first review with no finding starts no fix")
}

func TestReviewFix_ALedgerStopsTheItemOnADataDictThatHoldsTheKeyLedger(t *testing.T) {
	t.Parallel()

	run := runLedgerLoop(t, raisedOnce("[]", nil), ledgerExtra+`, data = {"ledger": "mine", "x": 1}`)

	require.Error(t, run.err)
	assert.Contains(t, run.err.Error(), "REFUSE")
	assert.Contains(t, run.err.Error(), "the key ledger")
}
