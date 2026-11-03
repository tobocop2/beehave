// Package recount counts the rows of an integration pull request body and checks that its heading and title say the same number.
// A heading or title written by hand drifts. The count from the rows is the one that is true.
package recount

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	row     = regexp.MustCompile(`^\| (#\d+|\[#\d+\]\([^)]*\)) \|`)
	heading = regexp.MustCompile(`^## (Merges after this description was first written|Pull requests merged into this branch|Merged pull requests)`)
	figure  = regexp.MustCompile(`\((\d+)( pull requests?)?\)`)
	title   = regexp.MustCompile(`^Remediation round [^:]*: (\d+) reviewed pull requests?, (\d+) issues$`)
)

// titlePrefix is the start of a title in the form of the tool. Any other title is a person's own, and the check leaves it alone.
const titlePrefix = "Remediation round "

// Tally is the count of the rows of a body: the pull requests merged and the issues closed.
type Tally struct {
	Rows   int
	Issues int
}

// Count reads the rows of the body. A pull request row starts with `| #N |` or a link to the pull request. An issue row is a line that starts with `- Closes #` or `| Closes #`.
func Count(body string) Tally {
	var tally Tally

	lines := strings.Split(body, "\n")

	for _, line := range lines {
		switch {
		case row.MatchString(line):
			tally.Rows++
		case strings.HasPrefix(line, "- Closes #"), strings.HasPrefix(line, "| Closes #"):
			tally.Issues++
		}
	}

	// With a known heading, the rows are those of its section, so the title and the heading count the same table. A second table elsewhere counts for nothing.
	if i := headingAt(lines); i >= 0 {
		tally.Rows = sectionRows(lines, i)
	}

	return tally
}

// headingAt returns the index of the first known heading, or -1.
func headingAt(lines []string) int {
	return slices.IndexFunc(lines, heading.MatchString)
}

// Check returns one finding for each count in the heading or the title that differs from the rows. A body with no known heading has nothing to check there.
func Check(titleText, body string) []string {
	found := checkHeading(strings.Split(body, "\n"))

	if strings.HasPrefix(titleText, titlePrefix) {
		found = append(found, checkTitle(titleText, Count(body))...)
	}

	return found
}

// number reads digits. The patterns give digits only, so a failure is a number too big for an int, and it reads as -1: it matches no count.
func number(digits string) int {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return -1
	}

	return n
}

// checkHeading compares the count in the first known heading with the rows of its section.
func checkHeading(lines []string) []string {
	for i, line := range lines {
		if !heading.MatchString(line) {
			continue
		}

		said := figure.FindStringSubmatch(line)
		if said == nil {
			return nil
		}

		have, got := number(said[1]), sectionRows(lines, i)
		if have == got {
			return nil
		}

		return []string{fmt.Sprintf("heading says %d but the section has %d rows", have, got)}
	}

	return nil
}

// sectionRows counts the pull request rows from the heading at index `from` to the next heading of the same level.
func sectionRows(lines []string, from int) int {
	n := 0

	for _, line := range lines[from+1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}

		if row.MatchString(line) {
			n++
		}
	}

	return n
}

func checkTitle(titleText string, tally Tally) []string {
	said := title.FindStringSubmatch(titleText)
	if said == nil {
		return []string{"title is not in the form `Remediation round <name>: <n> reviewed pull requests, <n> issues`"}
	}

	rows, issues := number(said[1]), number(said[2])

	if rows == tally.Rows && issues == tally.Issues {
		return nil
	}

	return []string{fmt.Sprintf("title says %d reviewed pull request%s, %d issues but the body has %d, %d", rows, plural(rows), issues, tally.Rows, tally.Issues)}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}

	return "s"
}

// Changed returns a finding for each asked count that did not move as one merge moves it. A merge adds exactly one row, and its closing rows add at least one issue.
// A write that left the count where it was did not land. A row removed and another added, or a row added twice, is not one row more.
func Changed(now, before Tally, rows, issues bool) []string {
	var found []string

	switch {
	case rows && now.Rows == before.Rows:
		found = append(found, fmt.Sprintf("the row count is still %d: the write did not land", now.Rows))
	case rows && now.Rows != before.Rows+1:
		found = append(found, fmt.Sprintf("the row count is %d but one merge adds one row to the %d before", now.Rows, before.Rows))
	}

	switch {
	case issues && now.Issues == before.Issues:
		found = append(found, fmt.Sprintf("the issue count is still %d: the write did not land", now.Issues))
	case issues && now.Issues < before.Issues:
		found = append(found, fmt.Sprintf("the issue count is %d but it was %d before: a closing row was removed", now.Issues, before.Issues))
	}

	return found
}
