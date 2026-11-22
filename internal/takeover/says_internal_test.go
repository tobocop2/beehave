package takeover

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSays_ATokenStandsAloneAsAWord(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]bool{
		"KEEP": true, "please KEEP.": true, "(KEEP)": true, "not\nKEEP\nit": true,
		"KEEP_ALL": false, "KEEPER": false, "keep": false, "": false,
	} {
		assert.Equal(t, want, says(keepWord, body), body)
	}

	assert.True(t, says(releaseWord, "RELEASE"))
	assert.False(t, says(releaseWord, "RELEASED"))
}

func TestSays_AnAccentedLetterOrDigitNextToTheTokenIsPartOfTheWord(t *testing.T) {
	t.Parallel()

	assert.False(t, says(keepWord, "KEEPé"))
	assert.False(t, says(keepWord, "éKEEP"))
	assert.False(t, says(releaseWord, "RELEASEé"))
	assert.False(t, says(releaseWord, "éRELEASE"))
	assert.False(t, says(keepWord, "KEEP2"))
	assert.True(t, says(keepWord, "é KEEP é"))
}

func TestStop_ARefusalReadsAsItsLinesOneUnderTheOther(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a\nb", stop(1, "a", "b").Error())
}

func TestBlock_LosesOnlyTheLastLineBreak(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "a\n", block("a\n\n"))
	assert.Equal(t, "a", block("a"))
	assert.NotEmpty(t, block(noticeMD))
	assert.NotEmpty(t, block(whyIdleMD))
	assert.NotEmpty(t, block(whyAssignedMD))
}
