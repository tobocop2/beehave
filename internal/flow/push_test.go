package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPush_TheEnginePushesTheHeadToTheBranchWithALease(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `first = push(name = "p", branch = "fix-7", head = "aaa", expect = "")
again = push(name = "p", branch = "fix-7", head = "bbb", expect = first.head)
command(run = [first.head, again.head])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{
		testProject + "|" + testItem + "|fix-7|aaa|",
		testProject + "|" + testItem + "|fix-7|bbb|aaa",
	}, f.pushed, "each push names the branch, the head and the head of the last push as the lease")
	assert.Equal(t, []string{"aaa bbb"}, h.commands.ran(), "push returns the head that the branch holds")
}

func TestPush_AReplayAfterThePushLandedDoesNotFail(t *testing.T) {
	t.Parallel()

	const call = testProject + "|" + testItem + "|fix-7|aaa|000"

	h, f := prHarness(t)
	deps := h.deps()
	deps.Forge = &crashForge{forge: f, crash: newCrash(t, h), on: "push"}

	crashAndResume(t, h, deps, `push(name = "p", branch = "fix-7", head = "aaa", expect = "000")`+"\n")

	assert.Equal(t, []string{call, call}, f.pushed, "the replay pushes the same head with the same lease, so a branch already at the head passes")
}

func TestPush_ABranchThatMovedStopsTheItem(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	f.pushErr = errFake

	_, err := walkBody(t, h, `push(name = "p", branch = "fix-7", head = "bbb", expect = "aaa")
command(run = ["after"])
`)
	require.ErrorIs(t, err, errFake)
	assert.Empty(t, h.commands.ran(), "no step runs after a refused push")
}

func TestPush_AnEmptyBranchOrHeadIsRefusedBeforeThePush(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"empty branch":          `push(branch = empty, head = "aaa")`,
		"empty head":            `push(branch = "fix-7", head = empty)`,
		"head that is a number": `push(branch = "fix-7", head = 1)`,
	} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, "empty = \"\"\n"+call+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.pushed, name)
	}
}

// heldComment is a forge whose first comment waits until the test lets it go, so the test can lose the claim of the run while the comment step runs.
type heldComment struct {
	forge

	entered chan struct{}
	hold    chan struct{}
	once    sync.Once
}

func (f *heldComment) Comment(ctx context.Context, project, item string, pr int, key, text string) (int, error) {
	f.once.Do(func() {
		f.entered <- struct{}{}
		hang(f.hold)
	})

	return f.forge.Comment(ctx, project, item, pr, key, text)
}

func TestPush_ARunThatLostItsClaimPushesNothing(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	held := &heldComment{forge: f, entered: make(chan struct{}, 1), hold: make(chan struct{})}
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	engine := launchedWith(t, h, func(deps *Deps) {
		deps.Forge = held
		clk.install(&deps.Clock)
	})

	done := runInBackground2(engine, writeFlow(t, bodyFlow(`pr_comment(text = "x", key = "k", name = "c")
push(name = "p", branch = "fix-7", head = "aaa")
`)))

	receive(t, held.entered)
	clk.advance(trustLimit)
	engine.beatClaims()
	close(held.hold)

	require.ErrorIs(t, receive(t, done).err, errClaimLost)
	assert.Empty(t, f.pushed, "the claim was lost before the push step, so the engine pushed nothing")
}

func TestPush_AResumeAfterThePushWasRecordedPushesNothingAgain(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	deps := h.deps()
	deps.Forge = &crashForge{forge: f, crash: newCrash(t, h), on: "comment"}

	crashAndResume(t, h, deps, `push(name = "p", branch = "fix-7", head = "aaa")
pr_comment(text = "x", key = "k", name = "c")
`)

	assert.Equal(t, []string{testProject + "|" + testItem + "|fix-7|aaa|"}, f.pushed, "the resume reads the recorded push and does not push again")
}

// heldPush is a forge whose push waits until its context ends, so the test can lose the claim of the run while the push runs.
type heldPush struct {
	forge

	entered chan struct{}
	ended   chan error
}

func (f *heldPush) Push(ctx context.Context, _, _, _, _, _ string) error {
	f.entered <- struct{}{}
	<-ctx.Done()
	f.ended <- context.Cause(ctx)

	return ctx.Err()
}

func TestPush_AClaimLostDuringThePushStopsThePush(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	held := &heldPush{forge: f, entered: make(chan struct{}, 1), ended: make(chan error, 1)}
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	engine := launchedWith(t, h, func(deps *Deps) {
		deps.Forge = held
		clk.install(&deps.Clock)
	})

	done := runInBackground2(engine, writeFlow(t, bodyFlow(`push(name = "p", branch = "fix-7", head = "aaa")
`)))

	receive(t, held.entered)
	clk.advance(trustLimit)
	engine.beatClaims()

	require.Error(t, receive(t, held.ended), "the end of the claim ended the context of the push")
	require.ErrorIs(t, receive(t, done).err, errClaimLost)
}
