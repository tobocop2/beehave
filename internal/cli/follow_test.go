package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

// Follow gives the entries of the test to emit and records the target. It ends with followErr.
func (s *fakeSteering) Follow(_ context.Context, target string, emit func(flow.FollowedEntry)) error {
	s.calls = append(s.calls, "follow "+target)

	for _, entry := range s.followed {
		emit(entry)
	}

	return s.followErr
}

func entry(phase string, try int, kind flow.EntryKind, text string) flow.FollowedEntry {
	return flow.FollowedEntry{Phase: phase, AgentEntry: flow.AgentEntry{Try: try, Kind: kind, Text: text}}
}

func TestStatusFollow_PrintsTheTextAndMarksTheToolCallsAndEachNewPhaseAndTry(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.followed = []flow.FollowedEntry{
		entry("p1", 1, flow.EntryText, "LINE-ONE"),
		entry("p1", 1, flow.EntryCall, "Bash: ls"),
		entry("p1", 2, flow.EntryText, "again"),
		entry("p2", 1, flow.EntryText, "next phase"),
	}

	code, stdout, stderr := rig.execute(t.Context(), "status", "fix-a", "--follow")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "-- p1, try 1\nLINE-ONE\n> Bash: ls\n-- p1, try 2\nagain\n-- p2, try 1\nnext phase\n", stdout)
	assert.Equal(t, []string{"follow fix-a"}, rig.steering.calls)
	assert.True(t, rig.steering.closeOK)
}

func TestStatusFollow_ReportsTheErrorOfTheFollow(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.followErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status", "fix-none", "--follow")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatusFollow_NeedsOneRunAndNoJSONOrItemFlag(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no_run":    {"status", "--follow"},
		"with_json": {"status", "fix-a", "--follow", "--json"},
		"with_item": {"status", "--item", "fix-a", "--follow"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)

			code, _, stderr := rig.execute(t.Context(), args...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "give one run id or item id with --follow")
			assert.Empty(t, rig.steering.calls)
		})
	}
}

func TestStatus_HelpNamesTheFollowFlag(t *testing.T) {
	t.Parallel()

	_, stdout, _ := newRig(t).execute(t.Context(), "status", "--help")

	assert.Contains(t, stdout, "--follow")
}
