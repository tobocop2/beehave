package brief_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

func check(t *testing.T, name string) brief.Report {
	t.Helper()

	report, err := brief.Check(name, brief.SizeOptions{})
	require.NoError(t, err)

	return report
}

func withReadFirst(paths ...string) string {
	return "## The item\n\nx\n\n## Read first\n\n- `" + strings.Join(paths, "`\n* `") + "` why\n\n## Names\n\n"
}

func TestCheck_ReportsTheRequiredReadingOfAFilledBrief(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	listed := put(t, dir, "plan.md", 500)
	text := withReadFirst(listed)
	name := writeFile(t, "brief.md", text)

	report := check(t, name)

	assert.Equal(t, "OK: the required reading of "+name+" is "+itoa(len(text)+500)+" bytes, at most 160000.", report.Out[len(report.Out)-1])
	assert.Empty(t, report.Warn)
}

func TestCheck_SizesTheReadFirstListOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	listed := put(t, dir, "plan.md", 100)
	named := put(t, dir, "other.md", 5000)
	text := withReadFirst(listed) + "Also see " + named + "\n"
	name := writeFile(t, "brief.md", text)

	report := check(t, name)

	assert.Equal(t, "OK: the required reading of "+name+" is "+itoa(len(text)+100)+" bytes, at most 160000.", report.Out[len(report.Out)-1])
}

func TestCheck_ABriefWithCRLFOrALoneCRIsCountedByteForByte(t *testing.T) {
	t.Parallel()

	listed := put(t, t.TempDir(), "p.md", 10)
	crlf := "## Read first\r\n\r\n- `" + listed + "` why\r\n"
	lone := "a\rb\r## Read first\r- `" + listed + "`\r"

	// The CRLF brief lists the file; the lone-CR brief is one line, so it lists nothing.
	for text, extra := range map[string]int{crlf: 10, lone: 0} {
		name := writeFile(t, "brief.md", text)

		report := check(t, name)

		assert.Equal(t, "OK: the required reading of "+name+" is "+itoa(len(text)+extra)+" bytes, at most 160000.", report.Out[len(report.Out)-1])
	}
}

func TestCheck_WarnsOnAFileAboveTheFileLimit(t *testing.T) {
	t.Parallel()

	for size, warns := range map[int]bool{brief.FileWarn: false, brief.FileWarn + 1: true} {
		path := put(t, t.TempDir(), "plan.md", size)
		name := writeFile(t, "brief.md", withReadFirst(path))

		report := check(t, name)

		if !warns {
			assert.Empty(t, report.Warn, size)

			continue
		}

		require.Len(t, report.Warn, 1)
		assert.Equal(t, "WARN: "+path+" is 8001 bytes, above 8000. List a digest of it, or the section the lane needs, under '## Read first'.", report.Warn[0])
	}
}

func TestCheck_RefusesAReadingAboveTheLimit(t *testing.T) {
	t.Parallel()

	path := put(t, t.TempDir(), "plan.md", brief.ReadLimit)
	name := writeFile(t, "brief.md", withReadFirst(path))

	report, err := brief.Check(name, brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the required reading is ")
	assert.NotEmpty(t, report.Warn)
	assert.NotContains(t, strings.Join(report.Out, "\n"), "OK:")
}

func TestCheck_ARequiredFileThatIsNoFileIsARefusal(t *testing.T) {
	t.Parallel()

	name := writeFile(t, "brief.md", withReadFirst("/no/such/file.md"))

	_, err := brief.Check(name, brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the required reading names /no/such/file.md, which is not a file here")
}

func TestCheck_AMissingBriefAndABadCapAreRefusals(t *testing.T) {
	t.Parallel()

	_, err := brief.Check("/no/brief.md", brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no brief at /no/brief.md")

	report, err := brief.Check(writeFile(t, "b.md", "x"), brief.SizeOptions{Cap: -1})

	require.NoError(t, err)
	require.Len(t, report.Warn, 1)
	assert.Contains(t, report.Warn[0], "the lane context cap is -1")
	assert.Contains(t, report.Out[0], "OK: ")
}

func TestCheck_AnExpectedBehaviourFileThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	nul := put(t, t.TempDir(), "ruling.md", 0)
	require.NoError(t, os.WriteFile(nul, []byte("a\x00b"), 0o600))

	_, err := brief.Check(writeFile(t, "brief.md", expectation("/s/a.sh", nul)), brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), nul+" holds a NUL byte")
}

func TestCheck_AListedFileThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	nul := put(t, t.TempDir(), "plan.md", 0)
	require.NoError(t, os.WriteFile(nul, []byte("a\x00b"), 0o600))

	_, err := brief.Check(writeFile(t, "brief.md", withReadFirst(nul)), brief.SizeOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), nul+" holds a NUL byte")
}

func TestCheck_AChainOutsideTheReadFirstListIsAWarning(t *testing.T) {
	t.Parallel()

	chained := put(t, t.TempDir(), "briefs/older.md", 10)
	name := writeFile(t, "brief.md", "Read "+chained+"\n")

	report := check(t, name)

	assert.Equal(t, "WARN: the reading of "+name+" names a file under a briefs/ directory: "+chained, report.Warn[0])
	assert.Contains(t, report.Out[len(report.Out)-1], "OK:")
}

func TestCheck_AFileTheSizeReportCannotReadBecomesAWarning(t *testing.T) {
	t.Parallel()

	nul := put(t, t.TempDir(), "nul.md", 0)
	require.NoError(t, os.WriteFile(nul, []byte("a\x00b"), 0o600))
	name := writeFile(t, "brief.md", "See "+nul+"\n")

	report := check(t, name)

	require.Len(t, report.Warn, 1)
	assert.Contains(t, report.Warn[0], nul+" holds a NUL byte")
	assert.Contains(t, report.Out[len(report.Out)-1], "OK:")
}

func expectation(acceptance, expected string) string {
	return "- Acceptance script: `" + acceptance + "`\n- Expected behavior: `" + expected + "`\n"
}

func TestCheck_WarnsWhenTheExpectedBehaviourIsMissingOrHollow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ text, why string }{
		{expectation("/s/a.sh", "[EXPECTED]"), "names an acceptance script and no expected behavior"},
		{expectation("/s/a.sh", "<fill>"), "names an acceptance script and no expected behavior"},
		{"- Acceptance script: `/s/a.sh`\n", "names an acceptance script and no expected behavior"},
		{expectation("/s/a.sh", "none:  "), "says 'none:' for the expected behavior, with no reason"},
	} {
		name := writeFile(t, "brief.md", tc.text)

		report := check(t, name)

		require.Len(t, report.Warn, 5, tc.text)
		assert.Equal(t, "WARN: "+name+" "+tc.why+".", report.Warn[0])
		assert.Contains(t, report.Warn[1], "When several outputs, formats, routes or settings render the changed value by different rules, write")
		assert.Contains(t, report.Warn[4], "'- Expected behavior: none: <reason>'.")
	}
}

func TestCheck_StaysQuietWhenTheExpectedBehaviourIsFineOrNeeded(t *testing.T) {
	t.Parallel()

	ruling := writeFile(t, "ruling.md", "## Expected behavior\n\nrows\n")

	for _, text := range []string{
		expectation("none: no output", "[EXPECTED]"),
		"- Result at the base: `x`\n",
		expectation("/s/a.sh", "<S>/rulings/item.md"),
		expectation("/s/a.sh", "none: one rule renders it"),
		expectation("/s/a.sh", ruling),
		expectation("/s/a.sh", "/no/such/ruling.md"),
	} {
		assert.Empty(t, check(t, writeFile(t, "brief.md", text)).Warn, text)
	}
}

func TestCheck_WarnsWhenTheExpectedFileHasNoHeading(t *testing.T) {
	t.Parallel()

	ruling := writeFile(t, "ruling.md", "no heading here\n")
	name := writeFile(t, "brief.md", expectation("/s/a.sh", ruling))

	report := check(t, name)

	require.NotEmpty(t, report.Warn)
	assert.Equal(t, "WARN: "+name+" names "+ruling+" for the expected behavior, and that file has no '## Expected behavior' heading.", report.Warn[0])
}

func TestCheck_TheReadFirstListTakesTheFirstWordOfEachDashOrStarItem(t *testing.T) {
	t.Parallel()

	a, b := put(t, t.TempDir(), "a.md", 1), put(t, t.TempDir(), "b.md", 2)
	text := "## Read first\n\n- `" + a + "` the plan\n* " + b + "\n-\n- \n\n## Other\n\n- /no/other.md\n"
	name := writeFile(t, "brief.md", text)

	report := check(t, name)

	assert.Equal(t, "OK: the required reading of "+name+" is "+itoa(len(text)+3)+" bytes, at most 160000.", report.Out[len(report.Out)-1])
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
