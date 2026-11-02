package flow

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/store/local"
)

// A claim in these tests is the real claim package over the real local store: one SQLite file that two runs share.
// The runs are two engines with their own state folders, as two obee processes have, so the claim row is the only thing they share.

const raceWait = 30 * time.Second

// claimingLife takes and gives back the claim of an item through the claim rows, and records the steps as the fake lifecycle does.
type claimingLife struct {
	*fakeLifecycle
	claims *claim.Claims
	owner  string
	start  func(context.Context) error
}

func (l *claimingLife) Claim(ctx context.Context, project, item string) (string, error) {
	if l.start != nil {
		if err := l.start(ctx); err != nil {
			return "", err
		}
	}

	if err := l.claims.Take(ctx, l.owner, item); err != nil {
		return "", err
	}

	return l.owner, l.step("claim", project, item)
}

func (l *claimingLife) Release(ctx context.Context, project, item, owner string) error {
	return errors.Join(l.claims.Release(ctx, owner, item), l.fakeLifecycle.Release(ctx, project, item, owner))
}

// sharedClaims opens the claim rows of one local store file for each name, and closes them at the end of the test.
func sharedClaims(t *testing.T, names ...string) []*claim.Claims {
	t.Helper()

	dir := t.TempDir()
	out := make([]*claim.Claims, 0, len(names))

	for range names {
		store, err := local.Open(t.Context(), dir, testProject, time.Now)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })

		claims, err := claim.New(claim.Options{Rows: store, Clock: time.Now, Out: io.Discard})
		require.NoError(t, err)

		out = append(out, claims)
	}

	return out
}

// engineWith builds and launches the engine of a rig over a lifecycle that the test chose.
func engineWith(t *testing.T, rig *agentRig, life lifecycle) *Engine {
	t.Helper()

	deps := rig.h.deps()
	deps.Lifecycle = life

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

type raceOutcome struct {
	who string
	err error
}

func TestExampleClaim_TwoRunsThatStartTogetherOnOneItemGiveOneClaimAndOneStop(t *testing.T) {
	t.Parallel()

	names := []string{"racerA", "racerB"}
	claims := sharedClaims(t, names...)
	proceed := make(chan struct{})

	var arrived sync.WaitGroup

	arrived.Add(len(names))

	barrier := make(chan struct{})

	go func() {
		arrived.Wait()
		close(barrier)
	}()

	start := func(ctx context.Context) error {
		arrived.Done()

		select {
		case <-barrier:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(raceWait):
			return errors.New("the other run never reached the claim")
		}
	}

	rigs := make([]*agentRig, len(names))
	lives := make([]*claimingLife, len(names))
	results := make(chan raceOutcome, len(names))

	engines := make([]*Engine, len(names))
	files := make([]string, len(names))

	// Both engines launch before either run starts: a launch can take longer than the wait at the barrier on a loaded host.
	for i, name := range names {
		rig, file := exampleRig(t, func(int) string { return "approve" })
		rig.h.agents.gate = proceed
		rigs[i] = rig
		files[i] = file
		lives[i] = &claimingLife{fakeLifecycle: rig.h.life, claims: claims[i], owner: name, start: start}
		engines[i] = engineWith(t, rig, lives[i])
	}

	for i, name := range names {
		go func() {
			_, err := engines[i].RunFile(testItem, testProject, files[i])
			results <- raceOutcome{who: name, err: err}
		}()
	}

	// The winner holds its agent in flight, so the one result that arrives first is the stop of the run that lost.
	var loser raceOutcome

	select {
	case loser = <-results:
	case <-time.After(raceWait):
		close(proceed)
		require.FailNow(t, "no run stopped: both runs hold the claim")
	}

	close(proceed)

	winner := <-results

	require.NoError(t, winner.err, "the run that took the claim goes on to the end")
	require.Error(t, loser.err)

	var refusal *claim.Error

	require.ErrorAs(t, loser.err, &refusal)
	assert.Equal(t, claim.CodeHeld, refusal.Code, "the loser stops with the held code")
	assert.Contains(t, loser.err.Error(), "held by session-"+winner.who, "the stop names the holder that won")

	for i, name := range names {
		started := len(rigs[i].h.agents.seen())

		if name == winner.who {
			assert.Positive(t, started, "the winner starts its agents")
			assert.Equal(t, []string{"claim:" + testItem, "ready:" + testItem, "release:" + testItem}, lives[i].seen())

			continue
		}

		assert.Zero(t, started, "the loser starts no agent")
		assert.Empty(t, lives[i].releasedBy(), "the loser releases nothing: it holds nothing")
	}
}

func TestExampleClaim_AnItemThatAnotherRunHoldsIsRefusedAndNoAgentStarts(t *testing.T) {
	t.Parallel()

	claims := sharedClaims(t, "holder", "latecomer")
	require.NoError(t, claims[0].Take(t.Context(), "holder", testItem))

	rig, file := exampleRig(t, func(int) string { return "approve" })
	life := &claimingLife{fakeLifecycle: rig.h.life, claims: claims[1], owner: "latecomer"}

	_, err := engineWith(t, rig, life).RunFile(testItem, testProject, file)

	var refusal *claim.Error

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, claim.CodeHeld, refusal.Code)
	assert.Empty(t, rig.h.agents.seen(), "a lane that another run holds starts no agent")

	require.NoError(t, claims[0].Release(t.Context(), "holder", testItem), "the first run still holds the row and can release it")
}
