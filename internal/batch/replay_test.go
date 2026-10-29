package batch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_GateOutputWithAnotherTreeFailsLoudly(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.gateTreeFixed = "t:zzz"

	_, err := Run(context.Background(), w.deps(), members("a"))

	require.EqualError(t, err, "the recorded gate ran on tree t:zzz, and the union is tree t:a")
}

func TestRun_TheRecordedGateInputHoldsTheTreeOfTheUnion(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := Run(context.Background(), w.deps(), members("a"))
	require.NoError(t, err)

	var env struct {
		Input gateInput `json:"input"`
	}
	require.NoError(t, json.Unmarshal(w.store.data["batch:q.gate#1"], &env))

	assert.Equal(t, "t:a", env.Input.Tree, "a gate record names the union it gated")
}

func TestRun_RecordedGateOutputWithAnotherTreeFailsOnReplay(t *testing.T) {
	t.Parallel()

	first := newWorld()
	_, err := Run(context.Background(), first.deps(), members("a"))
	require.NoError(t, err)

	var env rawEnvelope
	require.NoError(t, json.Unmarshal(first.store.data["batch:q.gate#1"], &env))

	env.Output, err = json.Marshal(GateOutcome{Tree: "t:other", Passed: true})
	require.NoError(t, err)

	first.store.data["batch:q.gate#1"], err = json.Marshal(env)
	require.NoError(t, err)

	replay := newWorld()
	replay.store = first.store
	_, err = Run(context.Background(), replay.deps(), members("a"))

	require.EqualError(t, err, "the recorded gate ran on tree t:other, and the union is tree t:a")
	assert.Empty(t, replay.gateTrees)
}

func TestRun_ReplayReturnsTheRecordedResultWithoutGitOrGate(t *testing.T) {
	t.Parallel()

	first := newWorld()
	first.bad = func(items []string) bool { return has(items, "b") }
	want, err := Run(context.Background(), first.deps(), members("a", "b", "c"))
	require.NoError(t, err)

	replay := newWorld()
	replay.store = first.store
	got, err := Run(context.Background(), replay.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, want.Merged, got.Merged)
	assert.Equal(t, want.Rejected, got.Rejected)
	assert.Equal(t, want.Gates, got.Gates)
	assert.Zero(t, replay.mergeTreeCalls)
	assert.Empty(t, replay.gateTrees)
}

func TestRun_ReplayWithOtherHeadsFailsLoudly(t *testing.T) {
	t.Parallel()

	first := newWorld()
	_, err := Run(context.Background(), first.deps(), members("a"))
	require.NoError(t, err)

	replay := newWorld()
	replay.store = first.store
	_, err = Run(context.Background(), replay.deps(), []Member{{Item: "a", Head: "h-moved"}})

	require.ErrorContains(t, err, "the record of batch:q.heads#1 ran on other input")

	delete(first.store.data, "batch:q.heads#1")

	_, err = Run(context.Background(), replay.deps(), []Member{{Item: "a", Head: "h-moved"}})

	require.ErrorContains(t, err, "the record of batch:q.union#1 ran on other input")
}

func TestRun_ReplayWithAnotherTipFailsLoudly(t *testing.T) {
	t.Parallel()

	first := newWorld()
	_, err := Run(context.Background(), first.deps(), members("a"))
	require.NoError(t, err)

	first.store.data["batch:q.tip#1"] = []byte(`{"input":{},"output":{"commit":"c:x","tree":"t:x"}}`)

	replay := newWorld()
	replay.store = first.store
	_, err = Run(context.Background(), replay.deps(), members("a"))

	require.ErrorContains(t, err, "the record of batch:q.union#1 ran on other input")
}

func TestRun_StepNamesCarryTheBatchNameAndACounter(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := Run(context.Background(), w.deps(), members("a"))

	require.NoError(t, err)
	assert.Equal(t, []string{"batch:q.heads#1", "batch:q.tip#1", "batch:q.union#1", "batch:q.gate#1", "batch:q.tip#2", "batch:q.tip#3"}, w.store.names)
}

func TestRun_StopsOnAFailureOfEachKind(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set     func(w *world)
		members []string
		message string
	}{
		"first tip read":    {func(w *world) { w.store.failOn["batch:q.tip#1"] = errBoom }, []string{"a"}, "batch:q.tip#1: boom"},
		"tip error":         {func(w *world) { w.failTip = errBoom }, []string{"a"}, "boom"},
		"first union":       {func(w *world) { w.store.failOn["batch:q.union#1"] = errBoom }, []string{"a"}, "batch:q.union#1: boom"},
		"gate step":         {func(w *world) { w.failGate = errBoom }, []string{"a"}, "boom"},
		"tip before merge":  {func(w *world) { w.store.failOn["batch:q.tip#2"] = errBoom }, []string{"a"}, "batch:q.tip#2: boom"},
		"merge":             {func(w *world) { w.failMerge = errBoom }, []string{"a"}, "merge a: boom"},
		"tip after merges":  {func(w *world) { w.store.failOn["batch:q.tip#3"] = errBoom }, []string{"a"}, "batch:q.tip#3: boom"},
		"first half":        {func(w *world) { w.store.failOn["batch:q.union#2"] = errBoom; w.bad = allBad }, []string{"a", "b"}, "batch:q.union#2: boom"},
		"second half":       {func(w *world) { w.store.failOn["batch:q.union#3"] = errBoom; w.bad = allBad }, []string{"a", "b"}, "batch:q.union#3: boom"},
		"git version":       {func(w *world) { w.failVersion = errBoom }, []string{"a"}, "boom"},
		"fold merge":        {func(w *world) { w.failMergeTree = 1 }, []string{"a"}, "boom"},
		"fold commit":       {func(w *world) { w.failNewCommit = errBoom }, []string{"a"}, "boom"},
		"conflict on tip":   {func(w *world) { w.conflict = pairConflict; w.failMergeTree = 3 }, []string{"a", "b"}, "boom"},
		"conflict culprit":  {func(w *world) { w.conflict = pairConflict; w.failMergeTree = 4 }, []string{"a", "b"}, "boom"},
		"retry union":       {func(w *world) { w.conflict = pairConflict; w.store.failOn["batch:q.union#2"] = errBoom }, []string{"a", "b"}, "batch:q.union#2: boom"},
		"an unreadable run": {func(w *world) { w.store.data["batch:q.tip#1"] = []byte("x") }, []string{"a"}, "the record of batch:q.tip#1 cannot be read"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			tc.set(w)

			_, err := Run(context.Background(), w.deps(), members(tc.members...))

			require.ErrorContains(t, err, tc.message)
		})
	}
}

func allBad(items []string) bool { return len(items) > 0 }

func TestBuildUnion_ConflictWithNoEarlierMemberNamesTheBase(t *testing.T) {
	t.Parallel()

	w := newWorld()
	f := folder{git: w, union: Union{Commit: "c:"}}

	with, err := f.culprit(context.Background(), Member{Item: "x", Head: "h-x"})

	require.NoError(t, err)
	assert.Equal(t, "base", with)
}

func TestBuildUnion_NamesTheLastMemberWhenOnlyTheFoldConflicts(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool { return item == "c" && len(have) > 1 }

	u, err := BuildUnion(context.Background(), w, Tip{Commit: "c:", Tree: "t:"}, members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []Held{{Member: Member{Item: "c", Head: "h-c"}, With: "b"}}, u.Held)
	assert.Equal(t, []string{"t:a", "t:a,b"}, u.Prefix)
	assert.Equal(t, "t:a,b", u.Tree)
}

func TestStep_Failures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mem := newMemory()

	var (
		number, again int
		channel       chan int
	)

	err := step(ctx, mem, "n", make(chan int), func() (int, error) { return 0, nil }, &number)
	require.ErrorContains(t, err, "record n")

	err = step(ctx, mem, "n", 1, func() (chan int, error) { return make(chan int), nil }, &channel)
	require.ErrorContains(t, err, "unsupported type")

	err = step(ctx, mem, "m", 1, func() (int, error) { return 0, errBoom }, &number)
	require.ErrorIs(t, err, errBoom)

	mem.data["bad"] = []byte(`{"input":1,"output":"text"}`)
	err = step(ctx, mem, "bad", 1, func() (int, error) { return 0, nil }, &number)
	require.ErrorContains(t, err, "the record of bad cannot be read")

	require.NoError(t, step(ctx, mem, "ok", 1, func() (int, error) { return 7, nil }, &number))
	assert.Equal(t, 7, number)

	require.NoError(t, step(ctx, mem, "ok", 1, func() (int, error) { return 0, errors.New("must not run") }, &again))
	assert.Equal(t, 7, again)
}
