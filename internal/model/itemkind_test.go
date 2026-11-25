package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestItemKind(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"2021": "gh", "0": "gh", "proj-a1b.2": "bd", "a-b": "bd", "A_1-x_y.z-9": "bd",
		"": "", "abc": "", "-x": "", "a-": "", "12a": "", "a b-c": "", "a-b c": "", "a/b-c": "", "٣": "", "_a-b": "", "a.b-c": "", "é-b": "",
	}
	for item, want := range tests {
		assert.Equal(t, want, model.ItemKind(item), item)
	}
}

func TestItemKind_ARoundIdHoldsTheSlashesOfItsBase(t *testing.T) {
	t.Parallel()

	for _, item := range []string{"round/main", "round/integration/s1", "round/integration/r1.2_x-y"} {
		assert.Equal(t, model.KindRound, model.ItemKind(item), item)
	}
}

func TestItemKind_AMalformedRoundIdIsNoItem(t *testing.T) {
	t.Parallel()

	for _, item := range []string{"round/", "round//s1", "round/s1/", "round/../x", "round/./x", "round/a b", "round/a:b", "round", "Round/x", "round/-x", "round/a!", "round/a/b b", "round/ab!", "round/é"} {
		assert.Empty(t, model.ItemKind(item), item)
	}
}

func TestSameItem_TwoSpellingsOfOneForgeNumberNameOneItem(t *testing.T) {
	t.Parallel()

	tooBig := "99999999999999999999"

	tests := map[string]struct {
		a, b string
		same bool
	}{
		"the same text":                            {"fix-login", "fix-login", true},
		"a forge number with a leading zero":       {"0412", "412", true},
		"two forge numbers of two values":          {"412", "413", false},
		"a forge number and a beads id":            {"412", "bb-412", false},
		"two beads ids that differ in case":        {"bb-7", "BB-7", false},
		"a number too large for an int":            {"0" + tooBig, tooBig, false},
		"the same number too large for an int":     {tooBig, tooBig, true},
		"a number that is the run of another text": {"#412", "412", false},
		"a signed number that is no item id":       {"+412", "412", false},
	}
	for name, test := range tests {
		assert.Equal(t, test.same, model.SameItem(test.a, test.b), name)
		assert.Equal(t, test.same, model.SameItem(test.b, test.a), name+", either order")
	}
}

func TestRoundBase_ReturnsTheBaseOfARoundIdAndNothingForAnyOtherItem(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base  string
		round bool
	}{
		"round/main": {"main", true}, "round/integration/s1": {"integration/s1", true},
		"12": {"", false}, "bb-1": {"", false}, "round/": {"", false}, "round/a b": {"", false},
	}
	for item, want := range tests {
		base, round := model.RoundBase(item)

		assert.Equal(t, want.base, base, item)
		assert.Equal(t, want.round, round, item)
	}
}
