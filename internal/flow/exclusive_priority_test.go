package flow

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgepkg "github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func TestExclusivePriority_AGateOfAPullRequestThatAPersonWaitsOnComesFirst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     string
		standing review.Standing
		readErr  error
		noForge  bool
		want     uint
	}{
		{"an_open_change_request", kindGate, review.Standing{ChangeRequests: []model.ChangeRequest{{Login: "rev", Commit: "abc"}}}, nil, false, priorityPersonWaits},
		{"an_unresolved_thread", kindGate, review.Standing{UnresolvedThreads: 1}, nil, false, priorityPersonWaits},
		{"nobody_waits", kindGate, review.Standing{}, nil, false, priorityNormal},
		{"a_closed_pull_request_with_a_thread", kindGate, review.Standing{Closed: true, UnresolvedThreads: 1}, nil, false, priorityNormal},
		{"a_merged_pull_request_with_a_thread", kindGate, review.Standing{Merged: true, UnresolvedThreads: 1}, nil, false, priorityNormal},
		{"a_merge_keeps_the_normal_place", kindMerge, review.Standing{UnresolvedThreads: 1}, nil, false, priorityNormal},
		{"a_batch_gate_keeps_the_normal_place", kindBatchGate, review.Standing{UnresolvedThreads: 1}, nil, false, priorityNormal},
		{"a_pull_request_that_cannot_be_read", kindGate, review.Standing{UnresolvedThreads: 1}, errFake, false, priorityNormal},
		{"no_forge", kindGate, review.Standing{}, nil, true, priorityNormal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.forge.state, harness.forge.err = test.standing, test.readErr

			deps := harness.deps()
			if test.noForge {
				deps.Forge = nil
			}

			engine, err := NewEngine(t.Context(), harness.opts, deps)
			require.NoError(t, err)

			t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

			in := ExclusiveInput{Kind: test.kind, Project: testProject, Item: "12"}
			standing, read := engine.gateStanding(t.Context(), in)

			assert.Equal(t, test.want, priorityOf(in, standing, read))
		})
	}
}

func TestExclusivePriority_TheWarningNamesOnlyAFailedReadOfAForge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		readErr  error
		wantWarn bool
	}{
		{"a_project_with_no_forge_says_nothing", fmt.Errorf("project p: %w", forgepkg.ErrNoForge), false},
		{"a_read_that_fails_warns", errFake, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.forge.err = test.readErr
			engine := harness.engine(t)

			var out bytes.Buffer

			engine.logger = slog.New(slog.NewTextHandler(&out, nil))

			_, read := engine.gateStanding(t.Context(), ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12"})

			assert.False(t, read)
			assert.Equal(t, test.wantWarn, strings.Contains(out.String(), "cannot read the pull request"))
		})
	}
}

func TestExclusivePriority_TheQueuedGateRecordsItsPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		standing review.Standing
		want     int
	}{
		{"a_thread_waits", review.Standing{UnresolvedThreads: 1}, int(priorityPersonWaits)},
		{"nobody_waits", review.Standing{}, int(priorityNormal)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.forge.state = test.standing
			engine := harness.engine(t)

			_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))
			require.NoError(t, err)

			rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName))
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, test.want, rows[0].Priority)
		})
	}
}
