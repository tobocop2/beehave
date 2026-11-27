package flow

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// nameSession makes the fake agent name the session id before it counts as in flight. An empty id names none.
func nameSession(harness *harness, session string) {
	harness.agents.mu.Lock()
	defer harness.agents.mu.Unlock()

	harness.agents.named = session
}

func TestPhase_TheSessionIdIsSavedAsAnEventWhileTheAgentRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)
	require.Len(t, phases, 1)

	// The saver writes on a goroutine of its own, so the event comes soon after the agent names the id.
	waitOn(t, waiting[struct{}]{what: "the session id to be saved as an event", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		session, err := dbos.GetEvent[string](client.flow, phases[0].ID, sessionEvent, 0)

		return struct{}{}, err == nil && session == "s-lost", nil
	}})
	assert.Equal(t, 1, harness.agents.running(), "the phase holds the session id while the agent still runs")

	// The log line follows the write, so it too comes soon after the event.
	waitOn(t, waiting[struct{}]{what: "the log line of the saved session", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))

		return struct{}{}, err == nil && strings.Contains(string(log), `msg="saved the session of the agent" item=`+testItem+" session=s-lost\n"), nil
	}})

	close(harness.agents.gate)
	require.NoError(t, receive(t, done).err)
}

func TestPhase_ALostStepThatNamedASessionResumesItOnTheReplay(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[0].Lost, "a first start follows no loss")
	require.NotNil(t, requests[1].Lost)
	assert.Equal(t, "s-lost", requests[1].Lost.Session)
	assert.Contains(t, requests[1].Lost.Continue, "after 33 minutes")
	assert.Contains(t, requests[1].Lost.Note, "This step ran before and was lost after 33 minutes.")
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt, "the note goes with the lost run, not into the prompt")
}

func TestPhase_ALostStepThatNamedNoSessionGetsTheNoteAndTheDiffSummary(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 33*time.Minute)
	// The lost agent committed one file before the engine stopped.
	commitIn(t, harness.trees.dir, "done.txt")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[1].Lost, "a loss before any session id has no session to resume")
	assert.Contains(t, requests[1].Prompt, "This step ran before and was lost after 33 minutes.")
	assert.Contains(t, requests[1].Prompt, "done.txt")
	assert.Contains(t, requests[1].Prompt, "1 file changed")
	assert.Contains(t, requests[1].Prompt, "git status")
}

func TestPhase_ALostStepWhoseDiffCannotBeReadStillResumesItsSession(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")
	// The lost agent committed one file and left a broken index, so git refuses the diff. The next run of the agent repairs the index.
	commitIn(t, harness.trees.dir, "done.txt")
	require.NoError(t, os.WriteFile(filepath.Join(harness.trees.dir, ".git", "index"), []byte("not an index"), 0o600))

	harness.agents.result = func(AgentRequest) agent.Result {
		// read-tree writes a new index from HEAD and does not read the broken one.
		gitIn(t, harness.trees.dir, "read-tree", "HEAD")

		return agent.Result{Outcome: agent.OutcomeExited}
	}

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	require.NotNil(t, requests[1].Lost, "the diff serves only the note, so its failure keeps the resume")
	assert.Equal(t, "s-lost", requests[1].Lost.Session)
	assert.Contains(t, requests[1].Lost.Note, "The engine could not read the diff summary of the changes since the step started.")
	assert.Contains(t, requests[1].Lost.Note, "Read `git status` and `git diff` before you start")

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "cannot read the diff summary of the lost agent step", "the log holds the reason")
}

func TestPhase_AFailedStepThatResumesStartsAFreshSession(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-failed")
	harness.agents.result = func(AgentRequest) agent.Result {
		return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
	}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)
	require.Len(t, phases, 1)

	session, err := dbos.GetEvent[string](client.flow, phases[0].ID, sessionEvent, 0)
	require.NoError(t, err)
	assert.Equal(t, "s-failed", session, "the failed step saved its session")

	harness.agents.mu.Lock()
	harness.agents.result, harness.agents.named = nil, ""
	harness.agents.mu.Unlock()

	_, resumed := resumeAndJoin(t, engine, client)

	_, err = dbos.GetEvent[string](client.flow, phases[0].ID, sessionEvent, 0)
	require.Error(t, err, "the rewind of beehest resume drops the session of the failed step")
	require.NoError(t, resumed)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[1].Lost, "a failed step was not lost, so its session is not resumed")
	assert.Empty(t, requests[1].Resume)
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt)
}

// loseTheRerun runs a limited run, stops the engine in the wait for the reset, then stops the next engine while the rerun runs.
// The rerun names session, or none for an empty session, and the claim store reports a last renewal ran after the start of the rerun.
func loseTheRerun(t *testing.T, h *harness, session string, ran time.Duration) {
	t.Helper()

	first := h.engine(t)
	done := runInBackground(first, testItem)

	waitOn(t, inASleep(t, first))
	require.NoError(t, first.Shutdown())
	receive(t, done)
	require.Len(t, h.agents.seen(), 1, "the engine stopped in the wait")

	h.agents.gate = make(chan struct{})
	nameSession(h, session)

	second, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, second.Launch())
	waitOn(t, waiting[struct{}]{what: "the agent of the rerun to run", run: runOfEngine(t, second), read: func() (struct{}, bool, error) { return struct{}{}, h.agents.running() == 1, nil }})

	if session != "" {
		waitOn(t, waiting[struct{}]{what: "the session of the rerun to be saved", run: runOfEngine(t, second), read: func() (struct{}, bool, error) {
			phases, err := dbos.ListWorkflows(second.ctx, dbos.WithFilterName(phaseName))
			if err != nil {
				return struct{}{}, false, nil
			}

			for _, phase := range phases {
				if saved, err := dbos.GetEvent[string](second.ctx, phase.ID, sessionEvent, 0); err == nil && saved == session {
					return struct{}{}, true, nil
				}
			}

			return struct{}{}, false, nil
		}})
	}

	// The start mark of the rerun reads the wall clock of the harness.
	h.beats.setRenewed(h.wall().Add(ran))
	shutdownOpeningGate(t, second, h.agents.gate)
	nameSession(h, "")
}

func TestPhase_ALostRerunThatNamedASessionResumesItsOwnSession(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)
	loseTheRerun(t, h, "s-rerun", 33*time.Minute)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[2].Lost)
	assert.Equal(t, "s-rerun", requests[2].Lost.Session, "the session of the lost rerun is newer than the limited one")
	assert.Contains(t, requests[2].Lost.Continue, "after 33 minutes")
	assert.Equal(t, "Work on the item.\n", requests[2].Prompt)
}

func TestPhase_ALostRerunThatNamedNoSessionResumesTheLimitedSessionAndKeepsTheLossNote(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)
	loseTheRerun(t, h, "", 33*time.Minute)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[2].Lost, "the lost rerun named no session, so the limited session is still the session of the step")
	assert.Equal(t, "sess-limited", requests[2].Lost.Session)
	assert.Contains(t, requests[2].Prompt, "This step ran before and was lost after 33 minutes.", "the note of the loss goes into the prompt")
	assert.Contains(t, requests[2].Lost.Note, limitSentence, "a fresh run gets the note of the limit too")
}

func TestPhase_ALimitAfterALostStartResumedItsSessionAndNamedNoneResumesTheLostSession(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))

	var limitedOnce atomic.Bool

	// The replay that resumes the lost session is limited before the agent names a session.
	h.agents.result = func(request AgentRequest) agent.Result {
		if request.Lost != nil && request.Lost.Session == "s-lost" && limitedOnce.CompareAndSwap(false, true) {
			return limitedIn("")
		}

		return exited
	}

	nameSession(h, "s-lost")
	loseAgent(t, h, 33*time.Minute)
	nameSession(h, "")

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[1].Lost)
	assert.Equal(t, "s-lost", requests[1].Lost.Session)
	require.NotNil(t, requests[2].Lost, "the limited run was given s-lost to resume and named none: prompt %q", requests[2].Prompt)
	assert.Equal(t, "s-lost", requests[2].Lost.Session)
	assert.Equal(t, "Work on the item.\n", requests[2].Prompt)
}

func TestClient_TheLostRecordHoldsTheSessionAndTheDiffSummary(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")
	commitIn(t, harness.trees.dir, "done.txt")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	client := harness.client(t, testVersion)
	lost := lostSteps(t, client)
	require.Len(t, lost, 1)

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)

	var record LostStep

	for _, phase := range phases {
		steps, err := dbos.GetWorkflowSteps(client.flow, phase.ID, dbos.WithStepsLoadOutput(true))
		require.NoError(t, err)

		for _, step := range steps {
			if raw, isText := step.Output.(string); isText && step.StepName == lostStepPrefix+"1" {
				require.NoError(t, json.Unmarshal([]byte(raw), &record))
			}
		}
	}

	assert.Equal(t, "s-lost", record.Session)
	assert.Contains(t, record.DiffStat, "done.txt")
	assert.Contains(t, record.DiffStat, "1 file changed")
	assert.Contains(t, record.Note, record.DiffStat, "the note quotes the diff summary")
}

func TestLostDiff_ReportsAFolderThatCannotBeFoundAndADiffThatGitRefuses(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, Deps{Trees: &fakeTrees{err: errFake}})
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, engine.Shutdown()) })

	summary, view, err := engine.lostDiff(t.Context(), PhaseInput{Item: testItem})
	require.NoError(t, err, "a call with no start commit has no summary")
	assert.Empty(t, summary)
	assert.Equal(t, viewNoStart, view)

	_, view, err = engine.lostDiff(t.Context(), PhaseInput{Item: testItem, StartHead: "abc"})
	require.ErrorContains(t, err, "find the folder of item "+testItem)
	assert.Equal(t, viewUnread, view)

	engine.deps.Trees = &fakeTrees{dir: t.TempDir()}
	_, view, err = engine.lostDiff(t.Context(), PhaseInput{Item: testItem, StartHead: "abc"})
	require.ErrorContains(t, err, "read the start mark of the item folder", "the folder is not a repository")
	assert.Equal(t, viewUnread, view)

	dir, head := gitTree(t)
	gitIn(t, dir, "update-ref", callStartRef, head)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o600))
	engine.deps.Trees = &fakeTrees{dir: dir}
	_, view, err = engine.lostDiff(t.Context(), PhaseInput{Item: testItem, StartHead: head})
	require.ErrorContains(t, err, "read the diff summary of the item folder", "the folder holds the mark, and git cannot read its index")
	assert.Equal(t, viewUnread, view)
}

func TestLoss_ALossStepThatCannotReadTheDiffSaysSoAndOneThatCannotReadTheSessionFails(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, Deps{Trees: &fakeTrees{err: errFake}})
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	in := PhaseInput{Item: testItem, StartHead: "abc"}

	lost, err := engine.loss(in, "phase-none", time.Now())(t.Context())
	require.NoError(t, err, "a folder that cannot be found changes the note and does not fail the step")
	assert.Empty(t, lost.DiffStat)
	assert.Contains(t, lost.Note, "The engine could not read the diff summary of the changes since the step started.")

	require.NoError(t, engine.Shutdown())

	_, err = engine.loss(in, "phase-none", time.Now())(t.Context())
	require.ErrorContains(t, err, "read the session of the lost agent step")
}

func TestSaveSession_AWriteThatFailsIsLoggedAndTheRunGoesOn(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	// A context that is no step of the library cannot hold an event.
	saver := engine.saveSession(t.Context(), PhaseInput{Item: testItem})
	saver.name("s-1")
	saver.finish()

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "cannot save the session of the agent")
	assert.Contains(t, string(log), "session=s-1")
	assert.NotContains(t, string(log), "saved the session of the agent", "a refused write is no save")
}

func TestSessionSaver_ANewIdNeverWaitsForAStoreThatBlocksAndTheNewestIdIsWrittenLast(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	var (
		mu      sync.Mutex
		written []string
	)

	// The store is down: each write waits until the test lets it go.
	saver := startSessionSaver(func(session string) {
		receive(t, release)
		mu.Lock()
		defer mu.Unlock()

		written = append(written, session)
	})

	named := make(chan struct{})

	go func() {
		defer close(named)

		for _, session := range []string{"s-1", "s-2", "s-3"} {
			saver.name(session)
		}
	}()

	// A new session id that waits for the store leaves the output of the agent unread, and this wait fails.
	receive(t, named)

	close(release)
	saver.finish()

	mu.Lock()
	defer mu.Unlock()

	require.NotEmpty(t, written)
	assert.Equal(t, "s-3", written[len(written)-1], "the event holds the newest id when the step ends")
}

func TestSessionSaver_TheStopWritesTheIdThatWaitsBehindAWriteThatBlocks(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		var (
			mu      sync.Mutex
			written []string
		)

		saver := startSessionSaver(func(session string) {
			receive(t, release)
			mu.Lock()
			defer mu.Unlock()

			written = append(written, session)
		})

		saver.name("s-1")
		synctest.Wait() // the saver waits in the write of s-1

		saver.name("s-2")

		finished := make(chan struct{})

		go func() {
			defer close(finished)

			saver.finish()
		}()

		synctest.Wait() // the stop is closed, and both the stop and the signal of s-2 wait
		close(release)
		receive(t, finished)

		mu.Lock()
		defer mu.Unlock()

		assert.Equal(t, []string{"s-1", "s-2"}, written, "the stop writes the id that waits, so the event holds the newest id when the step ends")
	})
}

// heldSaveHandler holds each record of a refused session write until release closes, and drops every other record.
type heldSaveHandler struct {
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (h heldSaveHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h heldSaveHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "cannot save the session of the agent" {
		h.once.Do(func() { close(h.entered) })
		hang(h.release)
	}

	return nil
}

func (h heldSaveHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h heldSaveHandler) WithGroup(string) slog.Handler { return h }

func TestRunAgent_TheAgentStepWaitsForTheWriteOfTheLastSessionIdBeforeItReturns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-last")
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, engine.Shutdown()) })

	synctest.Test(t, func(t *testing.T) {
		held := heldSaveHandler{entered: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
		// A context that is no step of the library refuses the write, and the log of the refusal stands in for a write that waits for the store.
		engine.logger = slog.New(held)

		returned := make(chan struct{})

		go func() {
			defer close(returned)

			_, runErr := engine.runAgent(t.Context(), t.Context(), &attempt{}, PhaseInput{Item: testItem}, nil)
			assert.NoError(t, runErr)
		}()

		synctest.Wait() // the saver waits in the held write, and the step waits for the saver unless it returned already
		receive(t, held.entered)

		select {
		case <-returned:
			assert.Fail(t, "the agent step returned while the write of the last session id waited, so DBOS records the output before the event")
		default:
		}

		close(held.release)
		receive(t, returned)
	})
}

func TestPhaseOutput_RecordsWhetherTheRunResumedALostSession(t *testing.T) {
	t.Parallel()

	assert.True(t, phaseOutput(agent.Result{Resumed: true}).Resumed)
	assert.False(t, phaseOutput(agent.Result{}).Resumed)
}

func TestLostNote_SaysThatNoTrackedFileChangedForAnEmptySummary(t *testing.T) {
	t.Parallel()

	lost := LostStep{Started: time.Unix(0, 0), Lost: time.Unix(0, 0).Add(5 * time.Minute)}

	assert.Contains(t, lost.note(viewDiff), "The engine found no change to tracked files since the step started.")
	assert.Contains(t, lost.continued(), "after 5 minutes")

	lost.DiffStat = " a.txt | 1 +\n 1 file changed, 1 insertion(+)"
	assert.Contains(t, lost.note(viewDiff), "The engine read this summary of the changes since the step started:\n\n```\n"+lost.DiffStat+"\n```\n\n")
	assert.Contains(t, LostStep{Started: time.Unix(100, 0)}.continued(), "after an unknown time")
}

func TestLostNote_ClaimsNoChangeAndNoSummaryWithoutTheStartMarkOrTheStartCommit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		view lostView
		says string
	}{
		{name: "no_start_commit", view: viewNoStart, says: "The engine has no start commit of the step, so it cannot say what the step changed."},
		{name: "other_tree", view: viewOtherTree, says: "The work tree is not the one that the step started in"},
		{name: "unread", view: viewUnread, says: "The engine could not read the diff summary of the changes since the step started."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A summary in the record stays out of the note: only the view of a folder that holds the mark quotes it.
			note := LostStep{Started: time.Unix(0, 0), DiffStat: " a.txt | 1 +"}.note(tc.view)

			assert.Contains(t, note, tc.says)
			assert.NotContains(t, note, "found no change")
			assert.NotContains(t, note, "a.txt")
		})
	}
}
