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
	Tests  string     `xml:"tests,attr"`
	Cases  []struct{} `xml:"testcase"`
	Suites []suite    `xml:"testsuite"`
}

// root is a report file: a testsuites element, or a testsuite element on its own.
type root struct {
	XMLName xml.Name
	suite
}

// countTests sums the tests of every testsuite in the report files that match pattern, a glob relative to dir.
// A file counts only if it was not in the snapshot before the gate, or its modification time or size differs from the snapshot. An unchanged file is stale.
// The count of a suite is its tests attribute, or its test cases when it has no attribute, plus the count of each suite inside it.
// A testsuites total is never read, so a total and its suites are not counted twice.
func countTests(dir, pattern string, before snapshot) (int, error) {
	files, err := reportFiles(dir, pattern, before)
	if err != nil {
		return 0, err
	}

	total := 0

	for _, file := range files {
		count, err := countFile(file)
		if err != nil {
			return 0, err
		}

		total += count
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

func countFile(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path matches the glob that the project names
	if err != nil {
		return 0, fmt.Errorf("read the test report %s: %w", path, err)
	}

	var parsed root
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return 0, fmt.Errorf("%w: the test report %s is not valid XML: %w", ErrNoTests, path, err)
	}

	switch parsed.XMLName.Local {
	case "testsuites":
		return sum(parsed.Suites), nil
	case "testsuite":
		return parsed.count(), nil
	}

	return 0, fmt.Errorf("%w: the test report %s starts with <%s>, not <testsuites> or <testsuite>", ErrNoTests, path, parsed.XMLName.Local)
}

func sum(suites []suite) int {
	total := 0
	for _, s := range suites {
		total += s.count()
	}

	return total
}

func (s suite) count() int {
	own, err := strconv.Atoi(s.Tests)
	if err != nil {
		own = len(s.Cases)
	}

	return own + sum(s.Suites)
}

// judge applies the test report of the project to a gate that exited 0. It returns the reason that the gate fails, or an empty string.
func judge(dir, pattern string, before snapshot) string {
	count, err := countTests(dir, pattern, before)
	if err != nil {
		return err.Error()
	}

	if count == 0 {
		return fmt.Sprintf("%v: the reports that match the test_report glob %q hold 0 tests", ErrNoTests, pattern)
	}

	return ""
}
