package gate

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrNoTests is the cause when a gate exits 0 but its test report shows that no test ran.
var ErrNoTests = errors.New("the gate ran no tests")

// suite is one testsuite element. A JUnit file nests suites to any depth, and a suite may carry its own count or only hold suites.
type suite struct {
	Tests    string `xml:"tests,attr"`
	Failures string `xml:"failures,attr"`
	Errors   string `xml:"errors,attr"`
	Cases    []struct {
		Failure *struct{} `xml:"failure"`
		Error   *struct{} `xml:"error"`
	} `xml:"testcase"`
	Suites []suite `xml:"testsuite"`
}

// tally is what the reports hold: the tests that ran and the tests that failed or errored.
type tally struct{ tests, failed int }

// root is a report file: a testsuites element, or a testsuite element on its own.
type root struct {
	XMLName xml.Name
	suite
}

// countTests sums the tests of every testsuite in the report files that match pattern, a glob relative to dir.
// A file counts only if it was not in the snapshot before the gate, or its modification time or size differs from the snapshot. An unchanged file is stale.
// The count of a suite is its tests attribute, or its test cases when it has no attribute, plus the count of each suite inside it.
// A testsuites total is never read, so a total and its suites are not counted twice.
// It also sums the failed tests: the failures and errors attributes of a suite, or its test cases with a failure or error element when that count is higher.
func countTests(dir, pattern string, before snapshot) (tally, error) {
	files, err := reportFiles(dir, pattern, before)
	if err != nil {
		return tally{}, err
	}

	var total tally

	for _, file := range files {
		count, err := countFile(file)
		if err != nil {
			return tally{}, err
		}

		total.tests += count.tests
		total.failed += count.failed
	}

	return total, nil
}

func reportFiles(dir, pattern string, before snapshot) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, fmt.Errorf("the test_report glob %q is not valid: %w", pattern, err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no file matches the test_report glob %q. The gate wrote no report", ErrNoTests, pattern)
	}

	fresh := changedSince(files, before)
	if len(fresh) == 0 {
		return nil, fmt.Errorf("%w: no file that matches the test_report glob %q changed during the run. The gate wrote no new report", ErrNoTests, pattern)
	}

	return fresh, nil
}

// stamp is the modification time and the size of a report file.
type stamp struct {
	modTime time.Time
	size    int64
}

// snapshot maps each file that the test_report glob matches to its stamp.
type snapshot map[string]stamp

// takeSnapshot records the stamp of each file that the glob matches. A bad glob or a file that cannot be read gives no entry; the check after the gate reports it.
func takeSnapshot(dir, pattern string) snapshot {
	taken := snapshot{}

	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return taken
	}

	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			taken[file] = stamp{modTime: info.ModTime(), size: info.Size()}
		}
	}

	return taken
}

// changedSince returns the files that were not in before, or whose modification time or size differs from before. No clock is read.
func changedSince(files []string, before snapshot) []string {
	changed := make([]string, 0, len(files))

	for _, file := range files {
		info, err := os.Stat(file)
		old, seen := before[file]

		if err == nil && (!seen || !info.ModTime().Equal(old.modTime) || info.Size() != old.size) {
			changed = append(changed, file)
		}
	}

	return changed
}

func countFile(path string) (tally, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path matches the glob that the project names
	if err != nil {
		return tally{}, fmt.Errorf("read the test report %s: %w", path, err)
	}

	var parsed root
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return tally{}, fmt.Errorf("%w: the test report %s is not valid XML: %w", ErrNoTests, path, err)
	}

	switch parsed.XMLName.Local {
	case "testsuites":
		total := sum(parsed.Suites)
		// The tests total of the root is never read, but its failure count is: it is the higher of the root and its suites, so a failure is not counted twice.
		total.failed = max(total.failed, parsed.failedAttrs())

		return total, nil
	case "testsuite":
		return parsed.count(), nil
	}

	return tally{}, fmt.Errorf("%w: the test report %s starts with <%s>, not <testsuites> or <testsuite>", ErrNoTests, path, parsed.XMLName.Local)
}

func sum(suites []suite) tally {
	var total tally

	for _, s := range suites {
		inner := s.count()
		total.tests += inner.tests
		total.failed += inner.failed
	}

	return total
}

func (s suite) count() tally {
	own, err := strconv.Atoi(s.Tests)
	if err != nil {
		own = len(s.Cases)
	}

	inner := sum(s.Suites)

	return tally{tests: own + inner.tests, failed: s.failedOwn() + inner.failed}
}

// failedOwn is the failed tests of the suite itself. The attributes and the case elements describe the same tests, so the higher count is taken and none is counted twice.
func (s suite) failedOwn() int {
	attrs := s.failedAttrs()
	cases := 0

	for _, c := range s.Cases {
		if c.Failure != nil || c.Error != nil {
			cases++
		}
	}

	return max(attrs, cases)
}

// failedAttrs is the failures and errors attributes of the suite.
func (s suite) failedAttrs() int {
	return countAttr(s.Failures) + countAttr(s.Errors)
}

// countAttr reads a count attribute. A missing or non-numeric attribute counts 0, and the test case elements still count the failures.
func countAttr(text string) int {
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return 0
	}

	return n
}

// judge reads the test report of the project after a gate that exited 0. It returns the status of the tests step and, when the step is not a pass,
// the reason. A report that is missing, stale, not valid or empty is no result line, so the step is unknown.
// A report that holds a failed or errored test fails the step, even when the gate command exited 0.
func judge(dir, pattern string, before snapshot) (Status, string) {
	count, err := countTests(dir, pattern, before)
	if err != nil {
		return StatusUnknown, err.Error()
	}

	if count.failed > 0 {
		return StatusFail, fmt.Sprintf("the gate exited 0, but the reports that match the test_report glob %q hold %d failed of %d tests", pattern, count.failed, count.tests)
	}

	if count.tests == 0 {
		return StatusUnknown, fmt.Sprintf("%v: the reports that match the test_report glob %q hold 0 tests", ErrNoTests, pattern)
	}

	return StatusPass, ""
}
