package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
)

// fakePreflight answers each call with result and err, and records what it was asked.
type fakePreflight struct {
	mu     sync.Mutex
	result preflight.Result
	err    error
	asked  []preflight.Request
	holds  []string
}

func (f *fakePreflight) Preflight(_ context.Context, project, item string, req preflight.Request, hold string) (preflight.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, req)
	f.holds = append(f.holds, project+"/"+item+"/"+hold)

	return f.result, f.err
}

func preflightHarness(t *testing.T, result preflight.Result) (*harness, *fakePreflight, *fakeWriter) {
	t.Helper()

	h := newHarness(t)
	fake := &fakePreflight{result: result}
	h.preflight = fake
	w := newFakeWriter()
	h.writer = w

	return h, fake, w
}

const reportFlow = `pf = preflight(name = "intake", head = "HEAD", base = "integration", hold = "hold")
item_comment(id = "1", key = "k", text = "%s")
`

func TestPreflight_AStopIsAResultTheWorkflowReads(t *testing.T) {
	t.Parallel()

	h, fake, w := preflightHarness(t, preflight.Result{Stop: preflight.Merged, Lines: []string{"STOP: merged", "  Recovery: branch again"}})

	_, err := walkBody(t, h, fmt.Sprintf(reportFlow, `" + str(pf.ok) + "|" + pf.stop + "|" + str(pf.code) + "|" + pf.reason + "`))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":k|False|MERGED|3|STOP: merged;   Recovery: branch again"}, w.notes)
	assert.Equal(t, []preflight.Request{{ExpectHead: "HEAD", ExpectBase: "integration"}}, fake.asked)
	assert.Equal(t, []string{testProject + "/" + testItem + "/hold"}, fake.holds)
}

func TestPreflight_AnUnchangedPullRequestGivesOkAndNoReason(t *testing.T) {
	t.Parallel()

	h, _, w := preflightHarness(t, preflight.Result{Lines: []string{"OK: nothing changed"}})

	_, err := walkBody(t, h, fmt.Sprintf(reportFlow, `" + str(pf.ok) + "|" + pf.stop + "|" + str(pf.code) + "|" + pf.reason + "`))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":k|True|UNCHANGED|0|"}, w.notes)
}

func TestPreflight_EveryStopCarriesItsOwnCode(t *testing.T) {
	t.Parallel()

	for _, stop := range preflight.Stops() {
		t.Run(stop.String(), func(t *testing.T) {
			t.Parallel()

			h, _, w := preflightHarness(t, preflight.Result{Stop: stop, Lines: []string{"line"}})

			_, err := walkBody(t, h, fmt.Sprintf(reportFlow, `" + str(pf.code) + "`))

			require.NoError(t, err)
			assert.Equal(t, []string{fmt.Sprintf("%s|1|%s:k|%d", testProject, testRun, stop.Code())}, w.notes)
		})
	}
}

func TestPreflight_ALeftOutKeywordChecksNothingOfThatKind(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, "preflight()\n")

	require.NoError(t, err)
	assert.Equal(t, []preflight.Request{{}}, fake.asked)
	assert.Equal(t, []string{testProject + "/" + testItem + "/"}, fake.holds)
}

func TestPreflight_TheOwnLoginsGoToTheReader(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, "preflight(own = {\"logins\": [\"owner\", \"bot\"], \"emails\": [\"o@x\"]})\n")

	require.NoError(t, err)
	assert.Equal(t, []preflight.Request{{Own: []string{"owner", "bot"}}}, fake.asked)
}

func TestPreflight_TheIdKeywordNamesTheItemToRead(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, `preflight(name = "a", id = "7", hold = "hold")
preflight(name = "b", hold = "hold")
`)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "/7/hold", testProject + "/" + testItem + "/hold"}, fake.holds)
}

func TestPreflight_AReadThatFailsEndsTheItemWithAnError(t *testing.T) {
	t.Parallel()

	h, fake, w := preflightHarness(t, preflight.Result{})
	fake.err = errFake

	_, err := walkBody(t, h, fmt.Sprintf(reportFlow, "x"))

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, w.notes, "the call after a failed read never runs")
}

func TestPreflight_ABuildWithNoReaderFailsAndNamesTheFix(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "preflight()\n")

	require.ErrorIs(t, err, errNoPreflight)
	assert.Contains(t, err.Error(), "workflow.star:")
}

func TestPreflight_AnUnknownKeywordFailsAtItsLine(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, "preflight(state = \"x\")\n")

	require.ErrorContains(t, err, "workflow.star:")
	assert.Empty(t, fake.asked)
}

func TestPreflight_TheStepIsRecordedUnderItsName(t *testing.T) {
	t.Parallel()

	h, _, _ := preflightHarness(t, preflight.Result{})
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("preflight(name = \"intake\")\n")))

	require.NoError(t, err)
	assert.Equal(t, []string{"preflight:intake#1"}, stepNames(t, engine))
}

func TestPreflight_AStopOfAStepWithNoNameIsReportedAsPreflight(t *testing.T) {
	t.Parallel()

	h, _, _ := preflightHarness(t, preflight.Result{Stop: preflight.Merged, Lines: []string{"STOP: merged"}})

	result, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("preflight()\n")))

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, "preflight", result.Stopped.Step)
	assert.Equal(t, preflight.Merged.Code(), result.Stopped.RC)
	assert.Equal(t, "STOP: merged", result.Stopped.Output)
}

// preflightOver is the preflight of the engine over a forge fake: the real checks, with the pull request that the fake holds.
type preflightOver struct{ world preflight.Reader }

func (p preflightOver) Preflight(ctx context.Context, _, _ string, req preflight.Request, hold string) (preflight.Result, error) {
	req.PR = testPull

	return preflight.Check(ctx, p.world, req, hold)
}

// goneBranch is a forge whose head branch is no longer on the remote.
type goneBranch struct{ *shipWorld }

func (goneBranch) BranchRef(context.Context, string) (string, bool, error) { return "", false, nil }

// preflightPull is the pull request of the example at head, with an empty label list, which the check needs.
func preflightPull(t *testing.T, head string, over map[string]any) model.PullRequest {
	t.Helper()

	row := map[string]any{"labels": []any{}, "author": map[string]any{"login": "owner"}}
	for key, value := range over {
		row[key] = value
	}

	return shipPull(t, head, row)
}

func TestExamplePreflight_AnItemThatChangedStartsNoAgentAndACleanItemStartsOne(t *testing.T) {
	t.Parallel()

	moved := strings.Repeat("e", 40)

	for name, tc := range map[string]struct {
		over   map[string]any
		expect string
		unset  bool
		login  string
		gone   bool
		agents bool
		stop   preflight.Stop
		reason string
	}{
		"a clean pull request starts the agents":                        {agents: true},
		"a merged pull request":                                         {over: map[string]any{"state": "MERGED"}, stop: preflight.Merged, reason: "already MERGED"},
		"a closed pull request":                                         {over: map[string]any{"state": "CLOSED"}, stop: preflight.Closed, reason: "CLOSED"},
		"a draft":                                                       {over: map[string]any{"isDraft": true}, stop: preflight.Draft, reason: "is a DRAFT"},
		"a pull request with the hold label":                            {over: map[string]any{"labels": []any{map[string]any{"name": "hold"}}}, stop: preflight.Held, reason: "hold"},
		"an open pull request of another author":                        {over: map[string]any{"author": map[string]any{"login": "dev"}}, stop: preflight.Claimed, reason: "dev"},
		"an own login that differs in case only":                        {login: "Owner", agents: true},
		"a head that moved":                                             {expect: moved, stop: preflight.HeadMoved, reason: "moved"},
		"a short EXPECT_HEAD":                                           {expect: "abc", stop: preflight.Refused},
		"a base that changed":                                           {over: map[string]any{"baseRefName": "main"}, stop: preflight.BaseChanged, reason: "main"},
		"a head branch that is gone":                                    {gone: true, stop: preflight.BranchGone, reason: "no longer exists"},
		"a merge and a hold: the merge is reported":                     {over: map[string]any{"state": "MERGED", "labels": []any{map[string]any{"name": "hold"}}}, stop: preflight.Merged, reason: "already MERGED"},
		"a close and a draft: the close is reported":                    {over: map[string]any{"state": "CLOSED", "isDraft": true}, stop: preflight.Closed, reason: "CLOSED"},
		"a hold and a draft: the hold is reported":                      {over: map[string]any{"isDraft": true, "labels": []any{map[string]any{"name": "hold"}}}, stop: preflight.Held, reason: "hold"},
		"a draft and a moved head: the draft is reported":               {over: map[string]any{"isDraft": true}, expect: moved, stop: preflight.Draft, reason: "is a DRAFT"},
		"a gone branch and a moved head: the branch is reported":        {gone: true, expect: moved, stop: preflight.BranchGone, reason: "no longer exists"},
		"the head the owner expected":                                   {expect: "$head", agents: true},
		"an operator who never set EXPECT_HEAD still starts the agents": {unset: true, agents: true},
		"an operator who never set EXPECT_HEAD still gets a real stop":  {unset: true, over: map[string]any{"state": "MERGED"}, stop: preflight.Merged, reason: "already MERGED"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := exampleRig(t, func(int) string { return "approve" })
			world := &shipWorld{pull: preflightPull(t, rig.head, tc.over)}

			rig.h.preflight = preflightOver{world}
			if tc.gone {
				rig.h.preflight = preflightOver{goneBranch{world}}
			}

			if tc.login != "" {
				rig.h.opts.Env["OWNER_LOGIN"] = tc.login
			}

			if tc.unset {
				delete(rig.h.opts.Env, "EXPECT_HEAD")
			} else {
				rig.h.opts.Env["EXPECT_HEAD"] = strings.ReplaceAll(tc.expect, "$head", rig.head)
			}

			result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			require.NoError(t, err, "a stop is a result and not an error")

			if tc.agents {
				assert.NotEmpty(t, rig.h.agents.seen())
				assert.Equal(t, "merged", result.Merge)
				assert.Nil(t, result.Stopped)

				return
			}

			assert.Empty(t, rig.h.agents.seen(), "a stop starts no agent")
			assert.Empty(t, result.Merge)
			require.NotNil(t, result.Stopped, "a stop says why in the result of the run")
			assert.Equal(t, "preflight:intake", result.Stopped.Step)
			assert.Equal(t, tc.stop.Code(), result.Stopped.RC)
			assert.Contains(t, result.Stopped.Output, tc.reason)
		})
	}
}
