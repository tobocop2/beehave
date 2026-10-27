package brief_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

func writeFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestReadSlots_TurnsEachKeyValueLineIntoASlot(t *testing.T) {
	t.Parallel()

	slots, err := brief.ReadSlots(writeFile(t, "slots.txt", "LANE=lane-one\nLANE=lane-two\nNOTE=a=b\n"))

	require.NoError(t, err)
	assert.Equal(t, []brief.Slot{
		{Name: "LANE", Value: "lane-one"},
		{Name: "LANE", Value: "lane-two"},
		{Name: "NOTE", Value: "a=b"},
	}, slots)
}

func TestReadSlots_SkipsALineWithNoKey(t *testing.T) {
	t.Parallel()

	slots, err := brief.ReadSlots(writeFile(t, "slots.txt", "=orphan\nno equals sign\nEMPTY=\n"))

	require.NoError(t, err)
	assert.Equal(t, []brief.Slot{{Name: "EMPTY", Value: ""}}, slots)
}

func TestReadSlots_NamesNoFileAsNoSlots(t *testing.T) {
	t.Parallel()

	slots, err := brief.ReadSlots("")

	require.NoError(t, err)
	assert.Empty(t, slots)
}

func TestReadSlots_RefusesAFileThatIsNotThere(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "none.txt")

	_, err := brief.ReadSlots(missing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no slots file at "+missing+". Check the path of the slots file.")
}

func TestReadSlots_ACRLFFileLeavesTheCarriageReturnInTheValue(t *testing.T) {
	t.Parallel()

	slots, err := brief.ReadSlots(writeFile(t, "slots.txt", "LANE=crlf\r\n"))

	require.NoError(t, err)
	assert.Equal(t, []brief.Slot{{Name: "LANE", Value: "crlf\r"}}, slots)
}

func TestReadSlots_ALoneCarriageReturnDoesNotEndALine(t *testing.T) {
	t.Parallel()

	slots, err := brief.ReadSlots(writeFile(t, "slots.txt", "LANE=a\rLANE=b\n"))

	require.NoError(t, err)
	assert.Equal(t, []brief.Slot{{Name: "LANE", Value: "a\rLANE=b"}}, slots)
}

func TestReadSlots_RefusesAFileWithANulByte(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "slots.txt", "LANE=a\x00b\n")

	_, err := brief.ReadSlots(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), path+" holds a NUL byte. Rewrite it as plain text.")
}

func TestFill_ReplacesEveryOccurrenceOfEachKey(t *testing.T) {
	t.Parallel()

	slots := []brief.Slot{{Name: "NAME", Value: "fix-1"}, {Name: "MIN", Value: "30"}}

	got := brief.Fill("# Lane `[NAME]`\nBudget: [MIN] for [NAME]\n", slots)

	assert.Equal(t, "# Lane `fix-1`\nBudget: 30 for fix-1\n", got)
}

func TestFill_TakesTheKeysOneAfterTheOther(t *testing.T) {
	t.Parallel()

	slots := []brief.Slot{{Name: "A", Value: "[B]"}, {Name: "B", Value: "end"}}

	assert.Equal(t, "end", brief.Fill("[A]", slots))
}

func TestFill_TheFirstOfTwoEqualKeysWins(t *testing.T) {
	t.Parallel()

	slots := []brief.Slot{{Name: "LANE", Value: "lane-one"}, {Name: "LANE", Value: "lane-two"}}

	assert.Equal(t, "lane-one", brief.Fill("[LANE]", slots))
}

func TestFill_ReturnsTheTextWhenThereAreNoSlots(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "keep [KEY]\n", brief.Fill("keep [KEY]\n", nil))
}

func TestFill_LeavesAnUnknownKeyAndTheCarriageReturnAlone(t *testing.T) {
	t.Parallel()

	got := brief.Fill("[LANE]\r\n[OTHER]", []brief.Slot{{Name: "LANE", Value: "crlf\r"}})

	assert.Equal(t, "crlf\r\r\n[OTHER]", got)
}

func TestCheckSlot_AcceptsAPathAndANoneWithAReason(t *testing.T) {
	t.Parallel()

	require.NoError(t, brief.CheckSlot("acceptance", "tests/accept.sh", "fix-1"))
	require.NoError(t, brief.CheckSlot("acceptance", "none: a comment only", "fix-1"))
}

func TestCheckSlot_RefusesABadValueAndNamesTheSlot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, value, fragment string
	}{
		{"empty", "", "the lane fix-1 has no acceptance; give a path with the acceptance field"},
		{"none-no-reason", "none:", "the acceptance field says 'none:' with no reason"},
		{"none-blank-reason", "none: \t\n", "the acceptance field says 'none:' with no reason"},
		{"bare-none", "none", "is a placeholder"},
		{"bracket", "[PATH]", "is a placeholder"},
		{"angle", "<path>", "is a placeholder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := brief.CheckSlot("acceptance", tc.value, "fix-1")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.fragment)
		})
	}
}

func TestCheckSlot_AnEmptySlotNamesTheBaseResultToo(t *testing.T) {
	t.Parallel()

	err := brief.CheckSlot("base_result", "", "fix-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no base_result")
	assert.Contains(t, err.Error(), "give a path with the base_result field, or 'none: <reason>'")
}
