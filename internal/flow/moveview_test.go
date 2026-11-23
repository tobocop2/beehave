package flow

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// theGap is a move of the run to this build, stopped between its cancel and its rewind.
type theGap struct {
	h      *harness
	engine *Engine
	client *Client
}

// inTheGap starts the move of the run of another build to this build, stops it after its cancel, runs read, and lets the move end. It returns the gap after the move.
func inTheGap(t *testing.T, read func(g theGap)) theGap {
	t.Helper()

	h, _ := otherBuildRun(t)
	g := theGap{h: h, engine: h.engine(t), client: h.client(t, otherBuild)}

	paused, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })

	t.Cleanup(release)

	var once sync.Once

	h.life.onClaim = func() { once.Do(func() { close(paused); <-resume }) }

	moved := make(chan error, 1)

	go func() { moved <- g.client.ResumeOnThisBuild(testItem) }()

	select {
	case <-paused:
	case err := <-moved:
		t.Fatalf("the move ended before the gap: %v", err)
	}

	held, err := g.client.store().Exact(dbos.WithFilterWorkflowIDs(testRun))
	require.NoError(t, err)
	require.Equal(t, dbos.WorkflowStatusCancelled, held[0].Status, "the store holds the cancel of the move, and the rewind has not run")

	read(g)
	release()
	require.NoError(t, receive(t, moved))

	return g
}

func TestStoreView_StatusShowsARunUnderAMoveAsOpenAtOnce(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		infos, err := g.client.Status()
		require.NoError(t, err)
		require.Len(t, infos, 1)

		assert.Equal(t, runRunning, infos[0].State, "a run under a move is open")
		assert.False(t, infos[0].Stranded, "the run goes to this build")
	})
}

func TestStoreView_FollowDoesNotEndForARunUnderAMove(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		ended, _, err := lookAt(g.client.store(), testRun)

		require.NoError(t, err)
		assert.False(t, ended, "status --follow ends the follow at the cancel of a move")
	})
}

func TestStoreView_ReplyIsNotRefusedAsToAnEndedRunWhileTheRunMoves(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		err := g.client.Reply(testItem, Reply{Text: "yes"})

		assert.NotContains(t, strings.ToLower(errText(err)), "waits for no answer", "reply refuses a run under a move as an ended run")
	})
}

func TestStoreView_ASendInTheGapLandsInTheInboxAndSurvivesTheRewind(t *testing.T) {
	t.Parallel()

	g := inTheGap(t, func(g theGap) {
		_, err := g.client.Send(testItem, Outgoing{Topic: "note", Sender: "me", Value: json.RawMessage(`"x"`)})

		require.NoError(t, err, "send is refused as to a run with no open run")
	})

	pending, err := runOfClient(t, g.client).messagePending()

	require.NoError(t, err)
	assert.True(t, pending, "the message that arrived in the gap waits in the inbox after the rewind")
}

func TestStoreView_ASecondJoinPlanJoinsTheRunUnderAMove(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		last, found, err := latestRun(g.client.store(), testItem)
		require.NoError(t, err)
		require.True(t, found)

		plan, err := planOf(last, found, ItemInput{Item: testItem, Project: testProject}, g.client.version, false)

		require.NoError(t, err)
		assert.Equal(t, attemptPlan{Join: testRun}, plan, "the plan joins the run and starts no attempt")
	})
}

func TestStoreView_TwoJoinsInTheGapStartNoSecondRun(t *testing.T) {
	t.Parallel()

	g := inTheGap(t, func(g theGap) {
		var group sync.WaitGroup

		ids := make(chan string, 2)

		for range 2 {
			group.Go(func() {
				handle, err := g.client.planAndHandle(context.Background(), ItemInput{Item: testItem, Project: testProject})
				if assert.NoError(t, err) {
					ids <- handle.GetWorkflowID()
				}
			})
		}

		group.Wait()
		close(ids)

		for id := range ids {
			assert.Equal(t, testRun, id, "a join starts a second run of the item")
		}
	})

	last, found, err := latestRun(g.client.store(), testItem)

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, last.Attempt, "the item has one run")
}

func TestStoreView_OpenRunReportsARunUnderAMoveAsOpen(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		open, err := g.client.OpenRun(testItem, func(string) bool { return false })

		require.NoError(t, err)
		assert.True(t, open.Open, "beehave run prints no pinned file for a run under a move")
	})
}

func TestStoreView_TheEnginePlanRefusesARunUnderAMove(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		spawned, planErr := g.engine.planOne(context.Background(), ItemInput{Item: testItem, Project: testProject}, testItem, nil)
		row, rowErr := g.engine.rowOf(context.Background(), model.Item{ID: testItem}, joinedPull{})

		require.NoError(t, rowErr)
		require.Error(t, planErr, "the engine plans a second run of the item")
		assert.Empty(t, spawned.Run)
		assert.Equal(t, queueRunning, row.State, "the queue offers the item again")
	})
}

func TestStoreView_TheJoinWaitDoesNotEndForARunUnderAMove(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		handle, err := retrieve[ItemResult](g.client.store(), testRun)
		require.NoError(t, err)

		watch := engineWatch{dir: g.client.dir, view: g.client.store(), item: testItem, grace: time.Hour, build: func() error { return nil }}

		type polled struct {
			done bool
			err  error
		}

		read := make(chan polled, 1)

		go func() {
			_, done, err := watch.poll(handle)
			read <- polled{done: done, err: err}
		}()

		// A poll that reads the run as open goes on to the check of the engine, which waits for probe.lock until the move ends.
		// A poll that reads the cancel as the end of the run answers at once.
		select {
		case got := <-read:
			assert.False(t, got.done, "the wait of a join takes the cancel of the move for the end of the run")
		case <-time.After(500 * time.Millisecond):
		}

		t.Cleanup(func() { <-read })
	})
}

func TestStoreView_TheOpenCountCountsARunUnderAMoveOnce(t *testing.T) {
	t.Parallel()

	inTheGap(t, func(g theGap) {
		open, err := g.engine.store().Open("", dbos.WithFilterName(itemName))

		require.NoError(t, err)
		require.Len(t, open, 1, "the cap admits one more run when the run under a move is not counted")
		assert.Equal(t, testRun, open[0].ID)
		assert.Equal(t, dbos.WorkflowStatusPending, open[0].Status)
	})
}

func TestStoreView_AReallyCanceledRunReadsAsEndedAtOnceWhileProbeLockIsHeld(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	probe, err := holdProbe(client.dir)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, probe.Unlock()) })

	within := func(what string, f func() error) {
		t.Helper()

		done := make(chan error, 1)

		go func() { done <- f() }()

		select {
		case err := <-done:
			require.NoError(t, err, what)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no answer within 2 s while probe.lock is held", what)
		}
	}

	within("status", func() error {
		infos, err := client.Status()
		if err == nil && (len(infos) != 1 || infos[0].State != runCancelled) {
			t.Errorf("status shows %v, want one canceled run", infos)
		}

		return err
	})

	within("reply", func() error {
		err := client.Reply(testItem, Reply{Text: "yes"})
		if !strings.Contains(errText(err), "waits for no answer") {
			t.Errorf("reply to a canceled run: %v", err)
		}

		return nil
	})

	within("plan", func() error {
		last, found, err := latestRun(client.store(), testItem)
		if err != nil {
			return err
		}

		plan, err := planOf(last, found, ItemInput{Item: testItem, Project: testProject}, client.version, false)
		assert.Equal(t, 2, plan.Attempt, "a canceled run plans the next attempt")

		return err
	})
}
