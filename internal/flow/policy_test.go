package flow

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextRound_FollowsTheRoundPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixed   int
		verdict Verdict
		want    roundAction
	}{
		{"approve_stops_at_the_first_review", 0, VerdictApprove, actionStop},
		{"approve_stops_after_the_last_round", 5, VerdictApprove, actionStop},
		{"round_1_resumes", 0, VerdictChanges, actionResume},
		{"round_2_resumes", 1, VerdictChanges, actionResume},
		{"round_3_resumes", 2, VerdictChanges, actionResume},
		{"round_4_starts_a_fresh_agent", 3, VerdictChanges, actionFresh},
		{"round_5_resumes_the_fresh_agent", 4, VerdictChanges, actionResume},
		{"after_round_5_the_owner_decides", 5, VerdictChanges, actionWaitOwner},
		{"the_owner_wait_repeats", 6, VerdictChanges, actionWaitOwner},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, nextRound(test.fixed, test.verdict))
		})
	}
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
