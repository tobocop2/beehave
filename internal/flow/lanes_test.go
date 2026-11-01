package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func statusRows(ids ...string) []dbos.WorkflowStatus {
	rows := make([]dbos.WorkflowStatus, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, dbos.WorkflowStatus{ID: id})
	}

	return rows
}

const lanesFlow = "def main(item):\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"first\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"look\", kind = \"review\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"again\", lane = \"rework\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"plan\", lane = \"design\")\n" +
	"    command(run = [\"lint\"], name = \"c\")\n"

func TestClient_LanesOfAnItemNameTheKindOfEachLaneInTheLedgerOfTheMethod(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	_, err := rig.run(t, lanesFlow, "x\n")
	require.NoError(t, err)

	lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
	require.NoError(t, err)

	assert.Equal(t, testItem, lanes.Item)

	var got [][2]string
	for _, lane := range lanes.Lanes {
		got = append(got, [2]string{lane.Run, lane.Kind})
	}

	assert.Equal(t, [][2]string{{testRun, "fix"}, {testRun, "review"}, {testRun, "rework"}, {testRun, "design"}}, got, "the command is not a lane")
}

func TestClient_LanesOfAnItemWithNoRunIsRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)

	_, err := rig.h.client(t, testVersion).Lanes("99")
	require.ErrorIs(t, err, errNoSuchRun)
}

func TestLanesOf_TheLanesOfEveryAttemptAreListedInTheOrderOfTheAttempts(t *testing.T) {
	t.Parallel()

	views := []RunSteps{
		{Run: "item-5-r1", Steps: []StepLine{{Name: "agent:a#1", Kind: "fix"}, {Name: "gate:g#1"}}},
		{Run: "item-5-r2", Steps: []StepLine{{Name: "agent:a#1", Kind: "rework"}, {Name: "agent:b#1", Kind: "review"}}},
	}

	lanes := lanesOf("5", views)

	require.Len(t, lanes.Lanes, 3)
	assert.Equal(t, "item-5-r1", lanes.Lanes[0].Run)
	assert.Equal(t, "item-5-r2", lanes.Lanes[1].Run)
	assert.Equal(t, "review", lanes.Lanes[2].Kind)
}

func TestAttemptOrder_AttemptTenComesAfterAttemptNineAndAForeignIdIsLeftOut(t *testing.T) {
	t.Parallel()

	rows := statusRows("item-5-r10", "item-5-r9", "item-5-r2", "item-5-rx")

	assert.Equal(t, []string{"item-5-r2", "item-5-r9", "item-5-r10"}, attemptOrder(rows, "item-5-r"))
}

func TestAgentLane_ALaneOutsideTheFiveIsRefusedAndAReviewCallTakesNoOtherLane(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"an_unknown_lane":         `lane = "triage"`,
		"a_lane_on_a_review_call": `lane = "fix", kind = "review"`,
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			flow := "def main(item):\n    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\", " + args + ")\n"

			_, err := rig.run(t, flow, "x")

			require.ErrorContains(t, err, "lane is")
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}

func TestClient_LanesReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Lanes(testItem)
	require.ErrorContains(t, err, "list the runs of item 12")
}

func TestLanesRead_StopsAtTheFirstRunThatCannotBeRead(t *testing.T) {
	t.Parallel()

	var seen []string

	_, err := lanesRead("x", []string{"a", "b", "c"}, func(id string) (RunSteps, error) {
		seen = append(seen, id)
		if id == "b" {
			return RunSteps{}, errNoSuchRun
		}

		return RunSteps{Run: id}, nil
	})

	require.ErrorIs(t, err, errNoSuchRun)
	assert.Equal(t, []string{"a", "b"}, seen)
}

func TestAgentArgs_CheckKindRefusesALaneOutsideTheFive(t *testing.T) {
	t.Parallel()

	err := agentArgs{lane: "triage"}.checkKind()
	require.ErrorContains(t, err, `lane is "triage". Use one of: fix, rework, relay, review, design`)
}
