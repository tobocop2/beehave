package flow

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

// readClosings is a workflow file that passes the issues of every earlier read of pr_closing named scope to the command seen, one group for each read, newest first, with a dot at the end.
// args are more arguments of the call.
func readClosings(args string) string {
	return "def main(item):\n    r = earlier_reads(kind = \"pr_closing\", name = \"scope\"" + args + ")\n" +
		"    command(name = \"seen\", run = [\"seen\", \"|\".join([\",\".join(x[\"issues\"]) for x in r.reads]) + \".\"])\n"
}

// runAs runs the file for the item in the next attempt and returns the error of the run.
func runAs(t *testing.T, engine *Engine, item, text string) error {
	t.Helper()

	_, err := engine.RunFile(item, testProject, writeFlow(t, text), modeOff())

	return err
}

// recordClosing is a workflow file that reads the closing issues once for each name, in order.
func recordClosing(names ...string) string {
	var text strings.Builder

	text.WriteString("def main(item):\n")

	for _, name := range names {
		text.WriteString("    pr_closing(name = \"" + name + "\")\n")
	}

	return text.String()
}

func TestEarlierReads_GivesTheReadsOfTheOtherRunsOfTheItemNewestRunFirstThenNewestStepFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closing = [][]int{{8}, {1}, {9}, {2}, {3}}
	engine := h.engine(t)

	require.NoError(t, runAs(t, engine, "9", recordClosing("scope")), "a run of another item")
	require.NoError(t, runAs(t, engine, testItem, recordClosing("scope", "other", "scope")), "the first run: two reads named scope and one named other")
	require.NoError(t, runAs(t, engine, testItem, recordClosing("scope")), "the second run")
	require.NoError(t, runAs(t, engine, testItem, readClosings("")))

	assert.Equal(t, []string{"#3|#2|#1."}, seenBy(t, h), "the second run comes first, then the steps of the first run from the last to the first; a read of another name and of another item is not in it")
}

func TestEarlierReads_ReadsTheRunsOfTheItemItNames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closing = [][]int{{8}, {1}}
	engine := h.engine(t)

	require.NoError(t, runAs(t, engine, "9", recordClosing("scope")))
	require.NoError(t, runAs(t, engine, testItem, recordClosing("scope")))
	require.NoError(t, runAs(t, engine, testItem, readClosings(`, item = "9"`)))

	assert.Equal(t, []string{"#8."}, seenBy(t, h), "the runs of item 9, not those of the item of the run")
}

func TestEarlierReads_LeavesOutAStepThatFailed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closing = [][]int{{5}}
	h.forge.closingOK = 1
	h.forge.closingErr = errFake
	engine := h.engine(t)

	require.NoError(t, runAs(t, engine, "9", recordClosing("scope")))
	require.ErrorIs(t, runAs(t, engine, "9", recordClosing("scope")), errFake, "the second run of item 9 fails at its read")
	require.NoError(t, runAs(t, engine, testItem, readClosings(`, item = "9"`)))

	assert.Equal(t, []string{"#5."}, seenBy(t, h), "the read of the failed run is not a read")
}

func TestEarlierReads_TheOwnRunIsInTheReadsOnlyWhenOwnRunIsTrue(t *testing.T) {
	t.Parallel()

	own := "def main(item):\n    pr_closing(name = \"scope\")\n    r = earlier_reads(kind = \"pr_closing\", name = \"scope\", own_run = %s)\n" +
		"    command(name = \"seen\", run = [\"seen\", \"|\".join([\",\".join(x[\"issues\"]) for x in r.reads]) + \".\"])\n"

	for name, tc := range map[string]struct {
		flag string
		want string
	}{
		"not_asked": {flag: "False", want: "#1."},
		"asked":     {flag: "True", want: "#2|#1."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.forge.closing = [][]int{{1}, {2}}
			engine := h.engine(t)

			require.NoError(t, runAs(t, engine, testItem, recordClosing("scope")), "the earlier run")
			require.NoError(t, runAs(t, engine, testItem, strings.Replace(own, "%s", tc.flag, 1)))

			assert.Equal(t, []string{tc.want}, seenBy(t, h), "the own run comes before the earlier run, and only when it is asked for")
		})
	}
}

func TestEarlierReads_AKindThatItDoesNotReadIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	err := runAs(t, newHarness(t).engine(t), testItem, "def main(item):\n    earlier_reads(kind = \"agent\", name = \"x\")\n")

	require.ErrorContains(t, err, `earlier_reads cannot read steps of kind "agent". Use one of: pr_closing, wait`)
}

func TestEarlierReads_GivesTheAnswersOfEarlierWaits(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"go?\")\n")
	engine := h.engine(t)

	first := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))
	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, first).err)

	read := "def main(item):\n    r = earlier_reads(kind = \"wait\", name = \"ask\")\n    command(name = \"seen\", run = [\"seen\", \"|\".join([x[\"answer\"].replace(\" \", \"_\") for x in r.reads]) + \".\"])\n"
	require.NoError(t, runAs(t, engine, testItem, read))

	assert.Equal(t, []string{"keep_#13."}, seenBy(t, h))
}

func TestEarlierReads_AnItemWithNoEarlierRunHasNoReads(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	require.NoError(t, runAs(t, h.engine(t), testItem, readClosings("")))

	assert.Equal(t, []string{"."}, seenBy(t, h), "an empty list, never None")
}

func TestEarlierReads_AReplayReturnsTheFirstReads(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closing = [][]int{{1}, {2}}

	file := writeFlow(t, "def main(item):\n    pr_closing(name = \"scope\")\n    r = earlier_reads(kind = \"pr_closing\", name = \"scope\", own_run = True)\n    pr_closing(name = \"scope\")\n    wait(name = \"ask\", question = \"go?\")\n"+
		"    command(name = \"seen\", run = [\"seen\", \"|\".join([\",\".join(x[\"issues\"]) for x in r.reads]) + \".\"])\n")
	interrupted(t, h, file)

	next := h.engine(t)
	done := runInBackground2(next, file)

	waitOn(t, ownerAsks(t, h))
	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "yes"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"#1."}, seenBy(t, h), "the reads of the first call come back, though the run recorded another read of the same name since")
}

func TestDryRun_AnEarlierReadsCallFindsNoEarlierRead(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	body := "def main(item):\n    r = earlier_reads(kind = \"pr_closing\", name = \"scope\", own_run = True)\n    if r.reads != []:\n        fail(\"WRONG READ: %r\" % r.reads)\n"
	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "earlier_reads:scope#1")
}

func TestEarlierReads_ACallWithAnArgumentOfTheWrongTypeIsRefusedByTheBuiltin(t *testing.T) {
	t.Parallel()

	_, err := (&script{}).earlierReads(starlark.NewBuiltin(earlierReadsKind, nil), nil, []starlark.Tuple{{starlark.String("own_run"), starlark.String("yes")}})

	require.ErrorContains(t, err, "earlier_reads")
}

func TestEarlierReads_AStoreThatIsShutDownRefusesTheRead(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = (&script{e: engine, in: ItemInput{Item: testItem}}).readEarlier(prClosingKind, "scope", testItem, false)
	require.ErrorContains(t, err, "list the earlier runs of item 12")

	_, err = collect[closingRead](engine.ctx, []string{"item-12-r1"}, "pr_closing:scope#")
	require.ErrorContains(t, err, "read the steps of item-12-r1")
}
