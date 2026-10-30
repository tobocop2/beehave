package gate_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
)

const (
	rootSuites = `<testsuites tests="9"><testsuite tests="2"><testcase/><testcase/></testsuite><testsuite tests="3"/></testsuites>`
	rootSuite  = `<testsuite tests="4"><testcase/></testsuite>`
	nested     = `<testsuites><testsuite tests="1"><testsuite tests="2"><testsuite tests="3"/></testsuite></testsuite></testsuites>`
	zeroTests  = `<testsuites tests="0"><testsuite tests="0"/></testsuites>`
	badXML     = `<testsuites><testsuite`
	noAttr     = `<testsuite><testcase/><testcase/></testsuite>`
)

// runWithReports runs a local gate that exits with code and writes files (name to content) into the tree.
func runWithReports(t *testing.T, code int, glob string, files map[string]string) (gate.Result, error) {
	t.Helper()

	dir := t.TempDir()
	shell := &fakeShell{code: code, onRun: func() {
		for name, content := range files {
			path := filepath.Join(dir, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			require.NoError(t, os.Chtimes(path, time.Now(), time.Now()))
		}
	}}
	g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})
	p := project("local")
	p.TestReport = glob

	return g.Run(context.Background(), gate.Request{Project: p, Dir: dir})
}

func TestReport_TheCountReadsEachShapeOfJUnitFile(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"a testsuites root: the suites are summed, the total is not": rootSuites,
		"a testsuite root":                                      rootSuite,
		"nested suites at any depth":                            nested,
		"a suite with no tests attribute counts its test cases": noAttr,
	} {
		result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": content})
		require.NoError(t, err, name)
		assert.True(t, result.Passed, name)
	}
}

func TestReport_ZeroTestsFailTheGateAndTheReasonSaysSo(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": zeroTests})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "the gate ran no tests")
	assert.Contains(t, result.Report, "hold 0 tests")
}

func TestReport_ATestsuitesTotalAndItsSuitesAreNotCountedTwice(t *testing.T) {
	t.Parallel()

	// The total says 5 but the suites hold none: the count is 0, so the gate fails.
	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": `<testsuites tests="5"><testsuite tests="0"/></testsuites>`})

	require.NoError(t, err)
	assert.False(t, result.Passed)
}

func TestReport_AFileThatDoesNotParseFailsTheGateAndNamesTheFile(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/bad.xml": badXML})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "bad.xml")
	assert.Contains(t, result.Report, "not valid XML")
}

func TestReport_AFileThatIsNotAReportFailsTheGate(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": `<html/>`})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "not <testsuites> or <testsuite>")
}

func TestReport_ANoMatchFailsTheGateAndSaysTheGateWroteNoReport(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", nil)

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "The gate wrote no report")
}

func TestReport_ManyFilesAreSummedAndOneEmptyFileDoesNotFailTheGate(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": zeroTests, "r/b.xml": rootSuite, "r/c.txt": badXML})

	require.NoError(t, err)
	assert.True(t, result.Passed, "b.xml holds 4 tests; c.txt does not match the glob")
}

func TestReport_AMatchThatCannotBeReadFailsTheGate(t *testing.T) {
	t.Parallel()

	// A folder named like a report matches the glob and cannot be read as a file.
	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/folder.xml/inner.txt": "x"})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "read the test report")
}

func TestReport_ABadGlobFailsTheGate(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/[", nil)

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "is not valid")
}

func TestReport_AGateThatExitsNonZeroFailsAsBeforeAndTheReportIsNotRead(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 1, "r/*.xml", map[string]string{"r/a.xml": badXML})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.NotContains(t, result.Report, "not valid XML")
}

// stamped is a report file with the modification time to give it. A zero time means the time of the write.
type stamped struct {
	content string
	mtime   time.Time
}

// runAgainstSnapshot writes before into the tree, runs a local gate that writes during, and returns the result.
func runAgainstSnapshot(t *testing.T, before, during map[string]stamped) gate.Result {
	t.Helper()

	dir := t.TempDir()
	write := func(files map[string]stamped) {
		for name, file := range files {
			path := filepath.Join(dir, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
			require.NoError(t, os.WriteFile(path, []byte(file.content), 0o600))
			mtime := file.mtime
			if mtime.IsZero() {
				mtime = time.Now()
			}
			require.NoError(t, os.Chtimes(path, mtime, mtime))
		}
	}
	write(before)

	g := build(&fakeShell{code: 0, onRun: func() { write(during) }}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})
	p := project("local")
	p.TestReport = "r/*.xml"

	result, err := g.Run(context.Background(), gate.Request{Project: p, Dir: dir})
	require.NoError(t, err)

	return result
}

func TestReport_AStaleReportWithTestsAndAGateThatWritesNoneFails(t *testing.T) {
	t.Parallel()

	hourAgo := time.Now().Add(-time.Hour)
	result := runAgainstSnapshot(t, map[string]stamped{"r/old.xml": {rootSuite, hourAgo}}, nil)

	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "The gate wrote no new report")
}

func TestReport_AReportWithAFutureTimestampAndNoRewriteFails(t *testing.T) {
	t.Parallel()

	result := runAgainstSnapshot(t, map[string]stamped{"r/old.xml": {rootSuite, time.Now().Add(time.Hour)}}, nil)

	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "The gate wrote no new report")
}

func TestReport_AReportRewrittenInTheSameSecondWithAnotherSizeCounts(t *testing.T) {
	t.Parallel()

	same := time.Now().Add(-time.Hour)
	result := runAgainstSnapshot(t,
		map[string]stamped{"r/a.xml": {zeroTests, same}},
		map[string]stamped{"r/a.xml": {rootSuite, same}})

	assert.True(t, result.Passed)
}

func TestReport_AReportRewrittenWithTheSameSizeAndAnotherTimeCounts(t *testing.T) {
	t.Parallel()

	result := runAgainstSnapshot(t,
		map[string]stamped{"r/a.xml": {rootSuite, time.Now().Add(-2 * time.Hour)}},
		map[string]stamped{"r/a.xml": {rootSuite, time.Now().Add(-time.Hour)}})

	assert.True(t, result.Passed)
}

func TestReport_ANewFileCountsWhateverItsAge(t *testing.T) {
	t.Parallel()

	result := runAgainstSnapshot(t,
		map[string]stamped{"r/old.xml": {rootSuite, time.Now().Add(-time.Hour)}},
		map[string]stamped{"r/new.xml": {rootSuite, time.Time{}}})

	assert.True(t, result.Passed)
}

func TestReport_AFreshReportWrittenWhileTheClockStepsBackwardCounts(t *testing.T) {
	t.Parallel()

	result := runAgainstSnapshot(t, nil, map[string]stamped{"r/new.xml": {rootSuite, time.Now().Add(-time.Hour)}})

	assert.True(t, result.Passed)
}

func TestReport_AnUnchangedFileDoesNotCountBesideANewReportThatHoldsNoTests(t *testing.T) {
	t.Parallel()

	result := runAgainstSnapshot(t,
		map[string]stamped{"r/old.xml": {rootSuite, time.Now().Add(-time.Hour)}},
		map[string]stamped{"r/new.xml": {zeroTests, time.Time{}}})

	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "hold 0 tests")
}

func TestReport_WithoutTheKeyTheGateIsUnchanged(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "", map[string]string{"r/a.xml": zeroTests})

	require.NoError(t, err)
	assert.True(t, result.Passed)
}
