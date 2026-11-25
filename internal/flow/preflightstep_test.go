package flow

import (
	"cmp"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

// fakePreflight answers each call with result and err, and records what it was asked.
type fakePreflight struct {
	noReads

	mu     sync.Mutex
	result preflight.Result
	err    error
	asked  []preflight.Request
	items  []string
}

func (f *fakePreflight) Preflight(_ context.Context, project, item string, req preflight.Request) (preflight.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, req)
	f.items = append(f.items, project+"/"+item)

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

const reportFlow = `pf = preflight(name = "intake", head = "HEAD", base = "integration")
item_comment(id = "1", key = "k", text = "%s")
`

func TestPreflight_AStopIsAResultTheWorkflowReads(t *testing.T) {
	t.Parallel()

	h, fake, w := preflightHarness(t, preflight.Result{Stop: preflight.Merged, Lines: []string{"STOP: merged", "  Recovery: branch again"}})

	_, err := walkBody(t, h, fmt.Sprintf(reportFlow, `" + str(pf.ok) + "|" + pf.stop + "|" + str(pf.code) + "|" + pf.reason + "`))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":k|False|MERGED|3|STOP: merged;   Recovery: branch again"}, w.notes)
	assert.Equal(t, []preflight.Request{{ExpectHead: "HEAD", ExpectBase: "integration"}}, fake.asked)
	assert.Equal(t, []string{testProject + "/" + testItem}, fake.items)
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
	assert.Equal(t, []string{testProject + "/" + testItem}, fake.items)
}

func TestPreflight_TheOldClaimArgumentsAreRefusedAndNameTheLibraryFunction(t *testing.T) {
	t.Parallel()

	const tail = ". The claim stops moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass "

	for keyword, value := range map[string]string{"hold": `"hold"`, "own": `{"logins": ["owner"]}`, "own_draft": "7"} {
		t.Run(keyword, func(t *testing.T) {
			t.Parallel()

			want := "preflight takes no argument " + keyword + tail + keyword + " to it."
			if keyword == "own_draft" {
				want = "preflight takes no argument own_draft. The draft stop moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass own_draft to it."
			}

			h, fake, _ := preflightHarness(t, preflight.Result{})

			_, err := walkBody(t, h, "preflight("+keyword+" = "+value+")\n")

			require.ErrorContains(t, err, want)
			assert.Empty(t, fake.asked, "a refused call never reaches the forge")

			// A splat brings the keyword past the static check, and the run refuses it with the same text.
			h, fake, _ = preflightHarness(t, preflight.Result{})

			_, err = walkBody(t, h, "guard = {\""+keyword+"\": "+value+"}\npreflight(**guard)\n")

			require.ErrorContains(t, err, want)
			assert.Empty(t, fake.asked, "a refused call through a splat never reaches the forge")
		})
	}
}

func TestPreflight_TheIdKeywordNamesTheItemToRead(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, `preflight(name = "a", id = "7")
preflight(name = "b")
`)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "/7", testProject + "/" + testItem}, fake.items)
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

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("preflight(name = \"intake\")\n")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"preflight:intake#1"}, stepNames(t, engine), "the read of the run item records one step, and the engine fixes no scope")
}

func TestPreflight_AStopOfAStepWithNoNameIsReportedAsPreflight(t *testing.T) {
	t.Parallel()

	h, _, _ := preflightHarness(t, preflight.Result{Stop: preflight.Merged, Lines: []string{"STOP: merged"}})

	result, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("preflight()\n")), modeOff())

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, "preflight", result.Stopped.Step)
	assert.Equal(t, preflight.Merged.Code(), result.Stopped.RC)
	assert.Equal(t, "STOP: merged", result.Stopped.Output)
}

// preflightOver is the preflight of the engine over a forge fake: the real checks, with the pull request that the fake holds.
type preflightOver struct{ world readWorld }

// readWorld is a forge fake that the checks and the claim reads can ask.
type readWorld interface {
	preflight.Reader
	IssueAssignees(ctx context.Context, repo string, number int) ([]string, error)
}

func (p preflightOver) Preflight(ctx context.Context, _, _ string, req preflight.Request) (preflight.Result, error) {
	req.PR = testPull

	return preflight.Check(ctx, p.world, req)
}

// ReadPull reads the pull request that the fake holds, as the reader of the engine does.
func (p preflightOver) ReadPull(ctx context.Context, _, _ string) (model.PullFacts, error) {
	view, err := p.world.PRView(ctx, testPull)
	if err != nil {
		return model.PullFacts{}, err
	}

	return view.Read(testPull)
}

// IssueAssignees reads the issue on the fake.
func (p preflightOver) IssueAssignees(ctx context.Context, _, repo string, number int) ([]string, error) {
	return p.world.IssueAssignees(ctx, repo, number)
}

// Origin finds an item that the owner wrote.
func (preflightOver) Origin(context.Context, string, string) (model.Origin, error) {
	return model.Origin{}, nil
}

// noReads is the part of a preflight fake that finds no pull request, no issue holder and no foreign author.
type noReads struct{}

func (noReads) ReadPull(context.Context, string, string) (model.PullFacts, error) {
	return model.PullFacts{}, nil
}

func (noReads) IssueAssignees(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}

func (noReads) Origin(context.Context, string, string) (model.Origin, error) {
	return model.Origin{}, nil
}

// goneBranch is a forge whose head branch is no longer on the remote.
type goneBranch struct{ *shipWorld }

func (goneBranch) BranchRef(context.Context, string) (string, bool, error) { return "", false, nil }

// preflightPull is the pull request of the example at head, with an empty label list, which the check needs.
func preflightPull(t *testing.T, head string, over map[string]any) model.PullRequest {
	t.Helper()

	row := map[string]any{"labels": []any{}, "assignees": []any{}, "author": map[string]any{"login": "owner"}}
	for key, value := range over {
		row[key] = value
	}

	return shipPull(t, head, row)
}

func TestExamplePreflight_AnItemThatChangedStartsNoAgentAndACleanItemStartsOne(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		over   map[string]any
		login  string
		gone   bool
		agents bool
		stop   codeOfStop
		step   string
		reason string
	}{
		"a clean pull request starts the agents":     {agents: true},
		"a merged pull request":                      {over: map[string]any{"state": "MERGED"}, stop: preflight.Merged, reason: "already MERGED"},
		"a closed pull request":                      {over: map[string]any{"state": "CLOSED"}, stop: preflight.Closed, reason: "CLOSED"},
		"a draft":                                    {over: map[string]any{"isDraft": true}, stop: stopDraft, step: "claim_stop:intake", reason: "is a DRAFT"},
		"a pull request with the hold label":         {over: map[string]any{"labels": []any{map[string]any{"name": "hold"}}}, stop: stopHeld, step: "claim_stop:intake", reason: "hold"},
		"an open pull request of another author":     {over: map[string]any{"author": map[string]any{"login": "dev"}}, stop: stopClaimed, step: "claim_stop:intake", reason: "dev"},
		"an own login that differs in case only":     {login: "Owner", agents: true},
		"a base that changed":                        {over: map[string]any{"baseRefName": "main"}, stop: preflight.BaseChanged, reason: "main"},
		"a head branch that is gone":                 {gone: true, stop: preflight.BranchGone, reason: "no longer exists"},
		"a merge and a hold: the merge is reported":  {over: map[string]any{"state": "MERGED", "labels": []any{map[string]any{"name": "hold"}}}, stop: preflight.Merged, reason: "already MERGED"},
		"a close and a draft: the close is reported": {over: map[string]any{"state": "CLOSED", "isDraft": true}, stop: preflight.Closed, reason: "CLOSED"},
		"a hold and a draft: the hold is reported":   {over: map[string]any{"isDraft": true, "labels": []any{map[string]any{"name": "hold"}}}, stop: stopHeld, step: "claim_stop:intake", reason: "hold"},
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
				rig.setRound("owner", tc.login)
			}

			result, err := runPipeline(rig.h.engine(t), file)

			require.NoError(t, err, "a stop is a result and not an error")

			if tc.agents {
				assert.NotEmpty(t, rig.h.agents.seen())
				assert.True(t, childOf(t, result).Approved)
				assert.Nil(t, result.Stopped)

				return
			}

			assert.Empty(t, rig.h.agents.seen(), "a stop starts no agent")
			assert.Empty(t, result.Merge)
			require.NotNil(t, result.Stopped, "a stop says why in the result of the run")
			assert.Equal(t, cmp.Or(tc.step, "preflight:intake"), result.Stopped.Step)
			assert.Equal(t, tc.stop.Code(), result.Stopped.RC)
			assert.Contains(t, result.Stopped.Output, tc.reason)
		})
	}
}
