package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCapHoldWait_TheWaitsDoubleFromTheFirstWaitToTheCeilingAndStayThere(t *testing.T) {
	t.Parallel()

	var waits []time.Duration

	for look := range 9 {
		waits = append(waits, clock{}.capHoldWait(look))
	}

	assert.Equal(t, []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour,
	}, waits)
}

func TestCapHoldWait_AHeldDayRecordsAboutThirtyLooksNotAboutTwoThousandNineHundred(t *testing.T) {
	t.Parallel()

	var elapsed time.Duration

	looks := 0

	for ; elapsed < 24*time.Hour; looks++ {
		elapsed += clock{}.capHoldWait(looks)
	}

	assert.Equal(t, 29, looks, "6 doubling waits and 23 waits of the ceiling fill the day")
}

func TestCapHoldWait_ALookCountFarPastTheCeilingNeitherOverflowsNorPassesTheCeiling(t *testing.T) {
	t.Parallel()

	assert.Equal(t, capHoldCeiling, clock{}.capHoldWait(1<<20))
}

func TestCapHoldWait_AFirstWaitPastTheCeilingIsCutToTheCeiling(t *testing.T) {
	t.Parallel()

	assert.Equal(t, capHoldCeiling, clock{CapWait: 3 * time.Hour}.capHoldWait(0))
}

func TestCapHoldWait_AFirstWaitFromTheClockStartsTheDoubling(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 20*time.Millisecond, clock{CapWait: 5 * time.Millisecond}.capHoldWait(2))
}
