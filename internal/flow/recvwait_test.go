package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_ARunInRecvShowsTheTopicAndTheDeadlineOfTheWait(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    recv(\"findings\", timeout = \"8s\", name = \"findings\")\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)

	before := time.Now()
	done := runInBackground(engine, testItem)
	read := newStatusRead(client)

	waitOn(t, waiting[struct{}]{what: "the open run to show the topic of its recv", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Receiving != "", nil
	}})

	info := onlyRun(t, client)
	assert.Equal(t, "findings", info.Receiving)
	assert.Equal(t, "recv:findings#1", info.Now, "the open step is the recv call, not the record of its deadline")
	assert.WithinDuration(t, before.Add(8*time.Second), info.Deadline, 3*time.Second)
	assert.Contains(t, info.Notes(), "waits for a message on topic findings until "+info.Deadline.Format(time.RFC3339))
	assert.Empty(t, info.Question, "a wait for a message asks the owner nothing")
	assert.False(t, info.Sleeping)

	view, err := client.Steps(info.ID)
	require.NoError(t, err)
	assert.Equal(t, "findings", view.Receiving, "the view of the steps of the run shows the wait too")
	assert.Equal(t, info.Deadline, view.Deadline)

	require.NoError(t, receive(t, done).err)

	after := onlyRun(t, client)
	assert.Empty(t, after.Receiving, "a run that got its timeout waits for nothing")
	assert.True(t, after.Deadline.IsZero())
}

func TestReceivingOf_ReadsTheDeadlineRecordOfTheRecvThatIsLast(t *testing.T) {
	t.Parallel()

	const window = `{"until":"2030-01-02T03:04:05Z","topic":"findings"}`

	until := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	record := func(name string, out any) dbos.StepInfo { return dbos.StepInfo{StepName: name, Output: out} }

	for name, tc := range map[string]struct {
		records []dbos.StepInfo
		want    recvWindow
		waits   bool
	}{
		"waiting":                       {[]dbos.StepInfo{record("recv:a#1.deadline", window), record("DBOS.sleep", "1")}, recvWindow{Until: until, Topic: "findings"}, true},
		"waiting_after_a_send":          {[]dbos.StepInfo{record("send:s#1", "{}"), record("DBOS.send", nil), record("recv#2.deadline", window)}, recvWindow{Until: until, Topic: "findings"}, true},
		"waiting_after_a_stashed_other": {[]dbos.StepInfo{record("recv:a#1.deadline", window), record("DBOS.sleep", "1"), record("DBOS.recv", "{}"), record("DBOS.sleep", "2")}, recvWindow{Until: until, Topic: "findings"}, true},
		"the_recv_ended":                {[]dbos.StepInfo{record("recv:a#1.deadline", window), record("DBOS.recv", "{}"), record("recv:a#1", "{}")}, recvWindow{}, false},
		"a_first_of_deadline":           {[]dbos.StepInfo{record("first_of:a#1.deadline", window), record("DBOS.sleep", "1")}, recvWindow{}, false},
		"a_gather_deadline":             {[]dbos.StepInfo{record("gather:a#1.deadline", window)}, recvWindow{}, false},
		"a_deadline_that_is_no_window":  {[]dbos.StepInfo{record("recv:a#1.deadline", "soon")}, recvWindow{}, false},
		"a_deadline_that_is_no_text":    {[]dbos.StepInfo{record("recv:a#1.deadline", 5)}, recvWindow{}, false},
		"a_recv_step_that_is_no_wait":   {[]dbos.StepInfo{record("recv:a#1", "{}"), record("DBOS.sleep", "1")}, recvWindow{}, false},
		"only_records_of_the_library":   {[]dbos.StepInfo{record("DBOS.sleep", "1")}, recvWindow{}, false},
		"no_record":                     {nil, recvWindow{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, waits := receivingOf(tc.records)

			assert.Equal(t, tc.waits, waits)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOpenName_ANameOfADeadlineRecordIsTheNameOfItsCallAndNotTheSendBefore(t *testing.T) {
	t.Parallel()

	pending := []dbos.StepInfo{{StepName: "DBOS.send"}, {StepName: "recv:findings#2.deadline"}, {StepName: "DBOS.sleep"}}

	assert.Equal(t, "recv:findings#2", openName(pending))
}

func TestDeadlineNote_AWaitInRecvSaysWhichTopicItWaitsOn(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	progress := RunProgress{Deadline: now.Add(time.Hour), Receiving: "findings"}

	assert.Equal(t, []string{"waits for a message on topic findings until 2026-11-03T11:00:00Z, 1h0m left"}, progress.NotesAt(now))
}
