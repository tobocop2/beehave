package gate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordsOf_SplitsOnNewlineOnlyAndDropsTheLastEmptyRecord(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b\r", "c"}, recordsOf("a\nb\r\nc\n"))
	assert.Equal(t, []string{"a"}, recordsOf("a"))
}

func TestFlatText_JoinsContinuedLinesAndFoldsBlanks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cargo test --all", flatText("cargo  test \\\n   --all"))
	assert.Equal(t, "a b c d", flatText(" a\tb\n\nc d "))
}

func TestFirstRunes_CutsOnRunesNotBytes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "ab", firstRunes("abc", 2))
	assert.Equal(t, "é", firstRunes("éa", 1))
	assert.Equal(t, "ab", firstRunes("ab", 5))
}

func TestIsAlnum_AcceptsAsciiLettersAndDigitsOnly(t *testing.T) {
	t.Parallel()

	for _, char := range []byte("azAZ09") {
		assert.True(t, isAlnum(char), string(char))
	}

	for _, char := range []byte("_-/ @[`{:") {
		assert.False(t, isAlnum(char), string(char))
	}
}

func TestHoldsToken_NeedsNoWordByteAfterAndBeforeWhenLeadIsSet(t *testing.T) {
	t.Parallel()

	assert.True(t, holdsToken("run name=fmt now", "name=fmt", false, isAlnum))
	assert.True(t, holdsToken("name=fmt", "name=fmt", true, isAlnum))
	assert.False(t, holdsToken("name=fmtx", "name=fmt", false, isAlnum))
	assert.False(t, holdsToken("xname=fmt", "name=fmt", true, isAlnum))
	assert.True(t, holdsToken("xname=fmt", "name=fmt", false, isAlnum))
	assert.True(t, holdsToken("name=fmtx name=fmt", "name=fmt", false, isAlnum))
	assert.False(t, holdsToken("other", "name=fmt", false, isAlnum))
	assert.False(t, holdsToken("ab", "b", true, isAlnum), "a token that ends the text with a word byte before it")
}
