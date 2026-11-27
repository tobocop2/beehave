package pool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/pool"
)

func TestHasRoom_EveryReadingKnownAndUnderBothCaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		readings map[string]model.Reading
		want     bool
	}{
		{"every_login_under_both_caps", map[string]model.Reading{"a": use(10, 10), "b": use(80, 90)}, true},
		{"no_reading_at_all", map[string]model.Reading{}, false},
		{"one_login_reads_unknown", map[string]model.Reading{"a": use(10, 10), "b": model.Unknown("no login")}, false},
		{"one_login_at_the_five_hour_cap", map[string]model.Reading{"a": use(10, 10), "b": use(90, 10)}, false},
		{"one_login_at_the_seven_day_cap", map[string]model.Reading{"a": use(10, 10), "b": use(10, 95)}, false},
		{"one_login_just_under_both_caps", map[string]model.Reading{"a": use(89.9, 94.9)}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, pool.HasRoom(test.readings, caps))
		})
	}
}

func TestHeadroom_ReadsTheRunnersOfTheAgentOnly(t *testing.T) {
	t.Parallel()

	list := append(runners("a", "b"), model.Runner{Name: "other", Agent: "opencode"})
	read := readerOf(map[string]model.Reading{"a": use(10, 10), "b": use(20, 20), "other": model.Unknown("no login")})

	assert.True(t, pool.Headroom(t.Context(), list, caps, "claude", read), "a runner of another agent does not count")
	assert.False(t, pool.Headroom(t.Context(), list, caps, "opencode", read), "the other agent reads unknown")
	assert.False(t, pool.Headroom(t.Context(), list, caps, "nobody", read), "an agent with no runner has no room")
}
