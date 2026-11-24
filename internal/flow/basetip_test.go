package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/batch"
)

// tipHost is a batch host that serves only the read of the tip of a base. The other methods of the host panic when a test calls them.
// tips are the answers in order, and the last one answers every later call. asked holds the project, the item and the base of each call.
type tipHost struct {
	batchHost

	mu    sync.Mutex
	tips  []batch.Tip
	err   error
	asked [][3]string
}

func (h *tipHost) Tip(_ context.Context, project, item, base string) (batch.Tip, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.asked = append(h.asked, [3]string{project, item, base})

	if h.err != nil || len(h.tips) == 0 {
		return batch.Tip{}, h.err
	}

	tip := h.tips[0]
	if len(h.tips) > 1 {
		h.tips = h.tips[1:]
	}

	return tip, nil
}

func (h *tipHost) calls() [][3]string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([][3]string{}, h.asked...)
}

// baseTipFlow is a workflow file whose main passes the commit and the tree that the call `call` read to the command seen.
func baseTipFlow(call string) string {
	return "def main(item):\n    t = " + call + "\n    command(name = \"seen\", run = [\"seen\", t.commit, t.tree])\n"
}

func TestBaseTip_ReturnsTheCommitAndTheTreeOfTheBase(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	host := &tipHost{tips: []batch.Tip{{Commit: "c0ffee", Tree: "7ee"}}}
	h.batch = host
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, baseTipFlow(`base_tip(base = "main", name = "t")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"c0ffee", "7ee"}, seenBy(t, h))
	assert.Equal(t, [][3]string{{testProject, testItem, "main"}}, host.calls(), "the call reads the base it names, once, for the project and the item of the run")
	assert.Contains(t, stepNames(t, engine), "base_tip:t#1")
}

func TestBaseTip_AReadThatFailsEndsTheStepAndNeverGivesAnEmptyTip(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.batch = &tipHost{err: errFake}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, baseTipFlow(`base_tip(base = "main")`)), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the tip of main")
	assert.Empty(t, h.commands.ranBy(testItem), "the run ends at the read")
}

func TestBaseTip_ATipWithNoCommitIsAFailedRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.batch = &tipHost{tips: []batch.Tip{{Tree: "7ee"}}}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, baseTipFlow(`base_tip(base = "main")`)), modeOff())

	require.ErrorIs(t, err, errEmptyTip)
	assert.Empty(t, h.commands.ranBy(testItem), "the run ends at the read")
}

func TestBaseTip_ABuildWithNoBatchHostFailsTheCall(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, baseTipFlow(`base_tip(base = "main")`)), modeOff())

	require.ErrorIs(t, err, errNoBaseTipReader)
}

func TestBaseTip_AnEmptyBaseIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	host := &tipHost{}
	h.batch = host

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, baseTipFlow(`base_tip(base = "")`)), modeOff())

	require.ErrorContains(t, err, "base must not be empty")
	assert.Empty(t, host.calls())
}

func TestBaseTip_ACallWithAnArgumentOfTheWrongTypeIsRefusedByTheBuiltin(t *testing.T) {
	t.Parallel()

	s := &script{e: &Engine{deps: Deps{Batch: &tipHost{}}}}

	_, err := s.baseTip(starlark.NewBuiltin(baseTipKind, nil), nil, []starlark.Tuple{{starlark.String("base"), starlark.MakeInt(9)}})

	require.ErrorContains(t, err, "base_tip")
}

func TestBaseTip_AReplayReturnsTheFirstReadNotAFreshOne(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, "def main(item):\n    t = base_tip(base = \"main\", name = \"t\")\n    wait(name = \"ask\", question = \"go?\")\n    command(name = \"seen\", run = [\"seen\", t.commit, t.tree])\n")
	h := newHarness(t)
	host := &tipHost{tips: []batch.Tip{{Commit: "first", Tree: "t1"}, {Commit: "second", Tree: "t2"}}}
	h.batch = host
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground2(next, file)

	waitOn(t, ownerAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "yes"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"first", "t1"}, seenBy(t, h), "the tip of the first read comes back, whatever the forge says now")
	assert.Len(t, host.calls(), 1, "the replay reads the forge no second time")
}
