package remedy_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/examples/remediation/tools/remedy"
)

const (
	laneHead = "abcdef1234567890abcdef1234567890abcdef12"
	laneRed  = "test empty_name_refused ... FAILED"

	goodLaneReport = "# Report\n\n## Claims\n| Claim | Where | Evidence | Verified at |\n|---|---|---|---|\n" +
		"| refuses an empty name | body | test empty_name_refused, red at base | " + laneHead + " |\n\n" +
		"## Hunks\n| Hunk | Arm | Red test line |\n|---|---|---|\n" +
		"| src/a.rs:10-14 | handler never calls validate() | " + laneRed + " |\n\n" +
		"## Inputs\n| Input class | Base | Head | Test |\n|---|---|---|---|\n" +
		"| empty string | accepted | refused 400 | empty_name_refused |\n\n" +
		"## Siblings\n| Site | Same behavior | Updated |\n|---|---|---|\n" +
		"| src/b.rs:40 create route | validates the same name | yes |\n\n" +
		"no background commands left\n"
)

func TestReport_AWholeReportPassesWithTheSummary(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "report", textFile(t, "report.md", goodLaneReport), "--head", laneHead)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "check-report: OK at abcdef123456 (claims 1, hunks 1, inputs 1, siblings 1)\n", out)
	assert.Empty(t, errOut)
}

func TestReport_AReportThatNamesAnotherHeadIsExitCodeOne(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, remedy.Deps{}, "report", textFile(t, "report.md", goodLaneReport), "--head", "1234567890abcdef1234567890abcdef12345678")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.NotContains(t, out, "check-report: OK")
	assert.NotEmpty(t, out)
}

func TestReport_AReportWithNoBackgroundLineIsExitCodeOne(t *testing.T) {
	t.Parallel()

	text := strings.Replace(goodLaneReport, "no background commands left\n", "", 1)

	code, out, _ := run(t, remedy.Deps{}, "report", textFile(t, "report.md", text), "--head", laneHead)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "no background commands left")
}

func TestReport_AMissingHeadAFileThatCannotBeReadAndABadCallAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	path := textFile(t, "report.md", goodLaneReport)

	for _, args := range [][]string{
		{"report", path},
		{"report"},
		{"report", path, "--head", laneHead, "extra"},
		{"report", path, "--nope"},
		{"report", filepath.Join(t.TempDir(), "none.md"), "--head", laneHead},
	} {
		code, out, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.NotEmpty(t, errOut, args)
	}
}

func TestPrintable_ALineWithABadByteIsPrintedAsText(t *testing.T) {
	t.Parallel()

	text := strings.Replace(goodLaneReport, laneRed, "green \x80 at base", 1)

	code, out, _ := run(t, remedy.Deps{}, "report", textFile(t, "report.md", text), "--head", laneHead)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "has no red test")
	assert.Equal(t, strings.ToValidUTF8(out, ""), out, "output is valid UTF-8")
}
