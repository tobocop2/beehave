package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestSlot_CheckAcceptsNoSlotAndAValidSlotAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		slot    model.Slot
		refused bool
	}{
		"no_slot":                 {slot: model.Slot{}},
		"a_plain_class":           {slot: model.Slot{Class: "mutation", Count: 1}},
		"a_class_with_dot_dash":   {slot: model.Slot{Class: "gate-2.x_y", Count: 4}},
		"a_class_with_a_slash":    {slot: model.Slot{Class: "a/b", Count: 1}, refused: true},
		"a_class_with_a_dot_dot":  {slot: model.Slot{Class: "..", Count: 1}, refused: true},
		"a_class_with_a_quote":    {slot: model.Slot{Class: "a'b", Count: 1}, refused: true},
		"a_class_and_no_count":    {slot: model.Slot{Class: "mutation"}, refused: true},
		"a_count_and_no_class":    {slot: model.Slot{Count: 2}, refused: true},
		"a_negative_count":        {slot: model.Slot{Class: "mutation", Count: -1}, refused: true},
		"a_class_with_a_newline":  {slot: model.Slot{Class: "a\nb", Count: 1}, refused: true},
		"a_class_that_ends_in_nl": {slot: model.Slot{Class: "a\n", Count: 1}, refused: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.slot.Check()

			if tc.refused {
				require.ErrorIs(t, err, model.ErrBadSlot)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestSlot_LimitedIsTrueForAnySlotThatIsNotTheZeroSlot(t *testing.T) {
	t.Parallel()

	assert.False(t, model.Slot{}.Limited())
	assert.True(t, model.Slot{Class: "mutation", Count: 1}.Limited())
	assert.True(t, model.Slot{Count: 1}.Limited(), "a count with no class is a slot that Check refuses, not no slot")
}
