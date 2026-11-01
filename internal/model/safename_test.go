package model_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

var onlySafe = regexp.MustCompile(`^[A-Za-z0-9._/-]*$`)

func TestSafeName_HoldsOnlyLettersDigitsAndFewMarks(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"issue-14", "review#5", "a b", `say "hi"`, "a/b", "..", "a.b", "a_b", "100%", "é", "it's", "a\\b", "$(x)", ""} {
		got := model.SafeName(name)

		assert.Regexp(t, onlySafe, got, name)
		assert.NotContains(t, got, "/", "a name never holds a separator: %q", name)
		assert.NotContains(t, got, ".", "a name never holds a dot: %q", name)
	}
}

func TestSafeName_NoTwoNamesShareASafeName(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}

	for _, name := range []string{"a#1", "a_231", "a b", "a_20b", "a/b", "a_2Fb", "a.b", "a_2Eb", "a_b", "a_5Fb", "a-b", "A", "a"} {
		got := model.SafeName(name)

		other, shared := seen[got]
		assert.False(t, shared, "%q and %q share %q", name, other, got)

		seen[got] = name
	}
}

func TestSafeName_LeavesAPlainNameAsItIs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "issue-14", model.SafeName("issue-14"))
	assert.Equal(t, "Item7", model.SafeName("Item7"))
}
