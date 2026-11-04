package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestRecords_SplitsOnNewlineOnlyAndDropsTheLastEmptyRecord(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b\r", "c"}, model.Records("a\nb\r\nc\n"))
	assert.Equal(t, []string{"a"}, model.Records("a"))
}

func TestFirstLine_SkipsBlankLinesAndTrimsTheLine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a b", model.FirstLine("  \n\n a b \r\nc\n"))
	assert.Empty(t, model.FirstLine(" \n "))
}

func TestBlankFields_SplitsOnSpaceTabAndNewlineOnly(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b", "c d"}, model.BlankFields(" a\tb\n\nc d "))
}

func TestFlatText_JoinsContinuedLinesAndFoldsBlanks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cargo test --all", model.FlatText("cargo  test \\\n   --all"))
	assert.Equal(t, "a b c d", model.FlatText(" a\tb\n\nc d "))
}

func TestFirstRunes_CutsOnRunesNotBytes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "ab", model.FirstRunes("abc", 2))
	assert.Equal(t, "é", model.FirstRunes("éa", 1))
	assert.Equal(t, "ab", model.FirstRunes("ab", 5))
}

func TestIsAlnum_AcceptsAsciiLettersAndDigitsOnly(t *testing.T) {
	t.Parallel()

	for _, char := range []byte("azAZ09") {
		assert.True(t, model.IsAlnum(char), string(char))
	}

	for _, char := range []byte("_-/ @[`{:") {
		assert.False(t, model.IsAlnum(char), string(char))
	}
}

func TestHoldsToken_NeedsNoWordByteAfterAndBeforeWhenLeadIsSet(t *testing.T) {
	t.Parallel()

	assert.True(t, model.HoldsToken("run name=fmt now", "name=fmt", false, model.IsAlnum))
	assert.True(t, model.HoldsToken("name=fmt", "name=fmt", true, model.IsAlnum))
	assert.False(t, model.HoldsToken("name=fmtx", "name=fmt", false, model.IsAlnum))
	assert.False(t, model.HoldsToken("xname=fmt", "name=fmt", true, model.IsAlnum))
	assert.True(t, model.HoldsToken("xname=fmt", "name=fmt", false, model.IsAlnum))
	assert.True(t, model.HoldsToken("name=fmtx name=fmt", "name=fmt", false, model.IsAlnum))
	assert.False(t, model.HoldsToken("other", "name=fmt", false, model.IsAlnum))
	assert.False(t, model.HoldsToken("ab", "b", true, model.IsAlnum), "a token that ends the text with a word byte before it")
}

func TestOneLine_JoinsEveryRunOfWhiteSpaceWithOneSpace(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a b c", model.OneLine("  a\n\tb   c \n"))
}

func TestClip_CutsAtACharacterNotAByte(t *testing.T) {
	t.Parallel()

	cut := model.Clip(strings.Repeat("é", 15), 10, "...")

	assert.Equal(t, strings.Repeat("é", 10)+"...", cut)
}

func TestClip_LeavesAShortTextWithNoMark(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a b", model.Clip("a\n b", 3, "..."))
}

func TestIsHomePath_AcceptsTildeAndTildeSlash(t *testing.T) {
	t.Parallel()

	assert.True(t, model.IsHomePath("~"))
	assert.True(t, model.IsHomePath("~/x"))
	assert.False(t, model.IsHomePath("~x"))
	assert.False(t, model.IsHomePath("/x"))
}
