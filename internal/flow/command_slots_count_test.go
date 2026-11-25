package flow

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runHostSlotItem runs one item of the project that the fake grant maps to the host, and returns the channel of its end. The item is one command of the program in the
// slot class "mutation".
func runHostSlotItem(t *testing.T, engine *Engine, item, host, program string) <-chan error {
	t.Helper()

	file := writeFlow(t, slotFlow(program, "mutation"))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(item, "on-"+host, file, modeOff())
		ended <- err
	}()

	return ended
}

func TestCommand_TwoHostsWithDifferentCountsForOneClassEachKeepTheirOwnCount(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("s1", "s2", "b1", "b2", "b3")
	h := newHarness(t)
	h.commands.before = jobs.hold
	h.slots = func(project, _, _ string) (SlotGrant, error) {
		host := strings.TrimPrefix(project, "on-")

		return SlotGrant{Host: host, Count: map[string]int{"big": 2, "small": 1}[host]}, nil
	}

	engine := h.engine(t)

	// The small host runs its one job, and its second job waits.
	small1 := runHostSlotItem(t, engine, "11", "small", "s1")
	require.Equal(t, "s1", receive(t, jobs.started))

	small2 := runHostSlotItem(t, engine, "12", "small", "s2")
	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 1 }))

	// The big host runs two jobs at once: its count is 2 although the small host asked for 1 in the same class.
	big1 := runHostSlotItem(t, engine, "13", "big", "b1")
	require.Equal(t, "b1", receive(t, jobs.started))

	big2 := runHostSlotItem(t, engine, "14", "big", "b2")
	require.Equal(t, "b2", receive(t, jobs.started), "the second job of the big host starts: its count is 2")

	// A third job of the small host after the big host called still waits: its count stays 1.
	small3 := runHostSlotItem(t, engine, "15", "small", "b3")
	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 2 }))

	assert.Equal(t, 1, entriesOf(jobs.order(), "start s1"))
	assert.Equal(t, 0, entriesOf(jobs.order(), "start s2"), "the small host keeps its count of 1")
	assert.Equal(t, 0, entriesOf(jobs.order(), "start b3"), "the small host keeps its count of 1")

	for _, program := range []string{"s1", "b1", "b2"} {
		jobs.let(program)
	}

	// The two waiting jobs of the small host run one after the other, in either order.
	first := receive(t, jobs.started)
	jobs.let(first)

	second := receive(t, jobs.started)
	jobs.let(second)

	assert.ElementsMatch(t, []string{"s2", "b3"}, []string{first, second})

	for _, ended := range []<-chan error{small1, small2, big1, big2, small3} {
		require.NoError(t, receive(t, ended))
	}
}

// stripSlotCount removes the count from the stored input of the one host job of the run. The store keeps an input as base64 text of its JSON.
// The job is found by its name, so a step that the engine adds before it does not move the test.
func stripSlotCount(t *testing.T, h *harness, run string) {
	t.Helper()

	db := sqlOf(t, h)

	var id, stored string

	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT workflow_uuid FROM workflow_status WHERE name = ? AND workflow_uuid LIKE ?", hostJobName, run+"-%").Scan(&id))
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT inputs FROM workflow_input WHERE workflow_uuid = ?", id).Scan(&stored))

	raw, err := base64.StdEncoding.DecodeString(stored)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"slot_count":1,`, "the stored input of the job holds the count")

	text := strings.Replace(string(raw), `"slot_count":1,`, "", 1)

	_, err = db.ExecContext(t.Context(), "UPDATE workflow_input SET inputs = ? WHERE workflow_uuid = ?", base64.StdEncoding.EncodeToString([]byte(text)), id)
	require.NoError(t, err)
}

func TestResume_AJobQueuedBeforeCountsWereRecordedIsRefusedWithTheRunTheStepAndTheFix(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("m1")
	h := newHarness(t)
	h.commands.before = jobs.hold
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	engine := h.engine(t)
	client := h.client(t, testVersion)

	first := runSlotItem(t, engine, testItem, slotFlow("m1", "mutation"))
	require.Equal(t, "m1", receive(t, jobs.started))

	require.NoError(t, client.Cancel(testItem))
	h.tick(t)
	require.Error(t, receive(t, first))

	// The stored input of the job loses its count, as the input of a job that an older build queued has none.
	stripSlotCount(t, h, testRun)

	err := client.Resume(testItem)

	require.ErrorContains(t, err, "reopen step", "the refusal names the step")
	require.ErrorContains(t, err, "item-12-r1", "the refusal names the run")
	require.ErrorContains(t, err, `slot class "mutation"`, "the refusal names the class")
	require.ErrorContains(t, err, "Run the item again from its start", "the refusal names the fix")
}

func TestReopen_ASlotJobGoesBackToTheQueueOfItsCountThatItsInputRecorded(t *testing.T) {
	t.Parallel()

	row := func(in ExclusiveInput) dbos.WorkflowStatus {
		text, err := json.Marshal(in)
		require.NoError(t, err)

		return dbos.WorkflowStatus{Name: hostJobName, Input: string(text)}
	}

	queue, partition, err := gateRewindTarget(row(ExclusiveInput{Kind: kindCommand, HostKey: hostKeyPrefix + "big", SlotCount: 2, Request: CommandRequest{SlotClass: "mutation"}}))
	require.NoError(t, err)
	assert.Equal(t, "slots:mutation:2", queue)
	assert.Equal(t, "host:big", partition)

	queue, _, err = gateRewindTarget(row(ExclusiveInput{Kind: kindCommand, HostKey: hostKeyPrefix + "small", SlotCount: 1, Request: CommandRequest{SlotClass: "mutation"}}))
	require.NoError(t, err)
	assert.Equal(t, "slots:mutation:1", queue)

	_, _, err = gateRewindTarget(row(ExclusiveInput{Kind: kindCommand, HostKey: hostKeyPrefix + "box", Request: CommandRequest{SlotClass: "mutation"}}))
	require.ErrorContains(t, err, "Run the item again from its start", "a job queued before the input held the count has no queue to go back to")
}
