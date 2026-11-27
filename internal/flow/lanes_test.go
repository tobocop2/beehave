package flow

import (
	"fmt"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
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
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"look\", label = \"review\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"again\", label = \"rework\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"plan\", label = \"design\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"hand\", label = \"relay\")\n" +
	"    command(run = [\"lint\"], name = \"c\")\n"

func TestClient_LanesOfAnItemNameTheLabelOfEachAgentStep(t *testing.T) {
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
		got = append(got, [2]string{lane.Run, lane.Label})
	}

	assert.Equal(t, [][2]string{{testRun, ""}, {testRun, "review"}, {testRun, "rework"}, {testRun, "design"}, {testRun, "relay"}}, got, "the command is not a lane")
}

func TestClient_LanesShowAFailedLaneWithItsLabelItsLoginAndItsModel(t *testing.T) {
	t.Parallel()

	for _, tc := range loginCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.reply("", agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"})

			_, err := rig.run(t, fmt.Sprintf(loginFlow, tc.keywords+` label = "review",`), "x\n")
			require.Error(t, err)

			lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
			require.NoError(t, err)
			require.Len(t, lanes.Lanes, 1)

			assert.Equal(t, stepFailed, lanes.Lanes[0].State)
			assert.Equal(t, []string{"review", tc.login, tc.model}, []string{lanes.Lanes[0].Label, lanes.Lanes[0].Login, lanes.Lanes[0].Model})
		})
	}
}

func TestClient_LanesShowARunningLaneWithItsLabelItsLoginAndItsModel(t *testing.T) {
	t.Parallel()

	for _, tc := range loginCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.h.agents.gate = make(chan struct{})
			release := sync.OnceFunc(func() { close(rig.h.agents.gate) })
			t.Cleanup(release)
			rig.reply(`{"verdict":"approve"}`, exited)

			engine := rig.h.engine(t)
			done := runInBackground2(engine, flowFile(t, fmt.Sprintf(loginFlow, tc.keywords+` label = "review",`), "Write to {{ .output_file }}\n"))
			waitOn(t, waiting[struct{}]{what: "the agent to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, rig.h.agents.running() == 1, nil }})

			lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
			require.NoError(t, err)
			require.Len(t, lanes.Lanes, 1)

			assert.Equal(t, stepRunning, lanes.Lanes[0].State)
			assert.Equal(t, []string{"review", tc.login, tc.model}, []string{lanes.Lanes[0].Label, lanes.Lanes[0].Login, lanes.Lanes[0].Model})

			release()
			require.NoError(t, receive(t, done).err)
		})
	}
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
		{Run: "item-5-r1", Steps: []StepLine{{Name: "agent:a#1", Label: "fix"}, {Name: "gate:g#1"}}},
		{Run: "item-5-r2", Steps: []StepLine{{Name: "agent:a#1", Label: "rework"}, {Name: "agent:b#1", Label: "review"}}},
	}

	lanes := lanesOf("5", views)

	require.Len(t, lanes.Lanes, 3)
	assert.Equal(t, "item-5-r1", lanes.Lanes[0].Run)
	assert.Equal(t, "item-5-r2", lanes.Lanes[1].Run)
	assert.Equal(t, "review", lanes.Lanes[2].Label)
}

func TestAttemptOrder_AttemptTenComesAfterAttemptNineAndAForeignIdIsLeftOut(t *testing.T) {
	t.Parallel()

	rows := statusRows("item-5-r10", "item-5-r9", "item-5-r2", "item-5-rx")

	assert.Equal(t, []string{"item-5-r2", "item-5-r9", "item-5-r10"}, attemptOrder(rows, "item-5-r"))
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
