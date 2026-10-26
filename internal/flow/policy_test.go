package flow

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestNextRound_FollowsTheRoundPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixed   int
		verdict Verdict
		want    Action
	}{
		{"approve_stops_at_the_first_review", 0, VerdictApprove, ActionStop},
		{"approve_stops_after_the_last_round", 5, VerdictApprove, ActionStop},
		{"round_1_resumes", 0, VerdictChanges, ActionResume},
		{"round_2_resumes", 1, VerdictChanges, ActionResume},
		{"round_3_resumes", 2, VerdictChanges, ActionResume},
		{"round_4_starts_a_fresh_agent", 3, VerdictChanges, ActionFresh},
		{"round_5_resumes_the_fresh_agent", 4, VerdictChanges, ActionResume},
		{"after_round_5_the_owner_decides", 5, VerdictChanges, ActionWaitOwner},
		{"the_owner_wait_repeats", 6, VerdictChanges, ActionWaitOwner},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, NextRound(test.fixed, test.verdict))
		})
	}
}

func TestParseVerdict_AcceptsKnownNamesAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	for _, verdict := range []Verdict{VerdictApprove, VerdictChanges} {
		got, err := ParseVerdict(string(verdict))
		require.NoError(t, err)
		assert.Equal(t, verdict, got)
	}

	_, err := ParseVerdict("maybe")
	require.ErrorIs(t, err, ErrUnknownVerdict)
	assert.ErrorContains(t, err, "use approve or changes")
}

func TestOrder_PutsReviewsThenContainerWorkThenTheRest(t *testing.T) {
	t.Parallel()

	items := []model.Item{
		{ID: "9", Severity: model.SeverityP0},
		{ID: "8", Severity: model.SeverityUnknown},
		{ID: "7", Severity: model.SeverityP2, Parent: "epic-1"},
		{ID: "6", Severity: model.SeverityP3},
		{ID: "5", Severity: model.SeverityP1, Parent: "epic-1"},
		{ID: "4", Severity: model.SeverityP1},
		{ID: "3", Severity: model.SeverityP1},
	}

	ordered := Order(items, map[string]bool{"6": true})

	var ids []string
	for _, item := range ordered {
		ids = append(ids, item.ID)
	}

	assert.Equal(t, []string{"6", "5", "7", "9", "3", "4", "8"}, ids)
	assert.Equal(t, "9", items[0].ID, "the input slice keeps its order")
}

func TestClosedItems_ReadsClosingKeywordsOnceInNumericOrder(t *testing.T) {
	t.Parallel()

	body := "Fixes #12 and closes #3.\nResolved: #12\nfix #100, Closed #7\nsee #55 and Closes #0 and closes #007"

	assert.Equal(t, []string{"3", "7", "12", "100"}, ClosedItems(body))
	assert.Empty(t, ClosedItems("no keywords #4"))
}

func TestLogger_RemovesSecretsFromMessagesAttributesErrorsAndGroups(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	log := newLogger(&out).With("scope", "sk-ant-abcdefgh01").WithGroup("g")
	log.Info("token sk-ant-abcdefgh02", "text", "sk-ant-abcdefgh03", "err", errors.New("sk-ant-abcdefgh04"),
		"nested", slog.Group("inner", slog.String("k", "sk-ant-abcdefgh05")), "count", 7)
	log.Debug("below the level sk-ant-abcdefgh06")

	text := out.String()
	assert.NotContains(t, text, "sk-ant-")
	assert.Equal(t, 5, strings.Count(text, "[redacted]"))
	assert.Contains(t, text, "count=7")
	assert.NotContains(t, text, "below the level")
}

func TestLogger_EnabledFollowsTheInnerLevel(t *testing.T) {
	t.Parallel()

	handler := newLogger(&bytes.Buffer{}).Handler()

	assert.False(t, handler.Enabled(context.Background(), slog.LevelDebug))
	assert.True(t, handler.Enabled(context.Background(), slog.LevelInfo))
}
