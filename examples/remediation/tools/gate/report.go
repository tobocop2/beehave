package gate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tobocop2/obee/internal/model"
)

type section string

const (
	sectionClaims   section = "Claims"
	sectionHunks    section = "Hunks"
	sectionInputs   section = "Inputs"
	sectionSiblings section = "Siblings"

	minHeadLength = 7
	headShown     = 12
	headerRows    = 2
	pipeMark      = "\x01"
	headingPrefix = "## "
	verifiedCell  = 3
	redTestCell   = 2
	updatedCell   = 2
	wideColumns   = 4
	narrowColumns = 3
)

var (
	placeholder    = regexp.MustCompile(`(^|[^A-Za-z])(TODO|TBD)([^A-Za-z]|$)|\?\?`)
	noBackground   = regexp.MustCompile(`^[ \t]*no background commands left\.?[ \t]*$`)
	listBullet     = regexp.MustCompile(`^[ \t]*-[ \t]+`)
	noneRow        = regexp.MustCompile(`^none: *[^ ]`)
	noRedTest      = regexp.MustCompile(`(uncovered|not run|(^|[^a-z_])green([^a-z_]|$))`)
	siblingUpdated = regexp.MustCompile(`^(yes|no: *[^ ])`)
	tableLine      = regexp.MustCompile(`^[ \t]*\|`)
	trailingPipe   = regexp.MustCompile(`\|[ \t]*$`)
	backtickSpan   = regexp.MustCompile("`[^`]*`")
	quotedSpan     = regexp.MustCompile(`"[^"]*"`)
)

func reportSections() []section {
	return []section{sectionClaims, sectionHunks, sectionInputs, sectionSiblings}
}

// columnsOf returns the column count of a report section, zero for a name that is not a section.
func columnsOf(name section) int {
	switch name {
	case sectionClaims, sectionInputs:
		return wideColumns
	case sectionHunks, sectionSiblings:
		return narrowColumns
	}

	return 0
}

// ReportResult holds the failures of a lane report, one line each, and the row count of each section.
type ReportResult struct {
	Failures []string
	rows     map[section]int
}

// OK reports whether the report has no failure.
func (r ReportResult) OK() bool {
	return len(r.Failures) == 0
}

// Summary is the line printed for a report that passes.
func (r ReportResult) Summary(head string) string {
	counts := make([]string, 0, len(reportSections()))
	for _, name := range reportSections() {
		counts = append(counts, fmt.Sprintf("%s %d", strings.ToLower(string(name)), r.rows[name]))
	}

	return fmt.Sprintf("check-report: OK at %s (%s)", model.FirstRunes(head, headShown), strings.Join(counts, ", "))
}

// splitRow splits a Markdown table row into trimmed cells; a pipe written as `\|` stays in its cell.
func splitRow(line string) []string {
	row := strings.ReplaceAll(line, `\|`, pipeMark)
	row = trailingPipe.ReplaceAllString(tableLine.ReplaceAllString(row, ""), "")

	if row == "" {
		return nil
	}

	cells := strings.Split(row, "|")
	for i, cell := range cells {
		cells[i] = strings.ReplaceAll(strings.Trim(cell, blanks), pipeMark, "|")
	}

	return cells
}

func isNoBackgroundLine(line string) bool {
	plain := strings.NewReplacer("`", "", "*", "").Replace(strings.ToLower(line))

	return noBackground.MatchString(listBullet.ReplaceAllString(plain, ""))
}

// cellFailures returns the failures of one row that has the right number of columns.
func cellFailures(name section, cells []string, head, where string) []string {
	var failures []string

	for number, cell := range cells {
		switch {
		case cell == "":
			failures = append(failures, fmt.Sprintf("%s: empty cell in column %d", where, number+1))
		case placeholder.MatchString(cell):
			failures = append(failures, fmt.Sprintf("%s: placeholder in column %d: %s", where, number+1, cell))
		}
	}

	return append(failures, sectionFailures(name, cells, head, where)...)
}

// sectionFailures returns the failures that only one section can have.
func sectionFailures(name section, cells []string, head, where string) []string {
	switch name {
	case sectionClaims:
		return staleClaim(cells, head, where)
	case sectionHunks:
		red := strings.ToLower(quotedSpan.ReplaceAllString(backtickSpan.ReplaceAllString(cells[redTestCell], ""), ""))
		if noRedTest.MatchString(red) {
			return []string{fmt.Sprintf("%s: %s has no red test (%s)", where, cells[0], cells[redTestCell])}
		}
	case sectionSiblings:
		if cells[updatedCell] != "" && !siblingUpdated.MatchString(cells[updatedCell]) {
			return []string{fmt.Sprintf(`%s: Updated must be "yes" or "no: <reason>", got "%s"`, where, cells[updatedCell])}
		}
	case sectionInputs:
	}

	return nil
}

func staleClaim(cells []string, head, where string) []string {
	if cells[verifiedCell] == "" {
		return nil
	}

	verified := strings.ReplaceAll(cells[verifiedCell], "`", "")
	if utf8.RuneCountInString(verified) >= minHeadLength && (strings.HasPrefix(head, verified) || strings.HasPrefix(verified, head)) {
		return nil
	}

	return []string{fmt.Sprintf(`%s: verified at %s, not at the head %s ("%s")`, where, cells[verifiedCell], model.FirstRunes(head, headShown), cells[0])}
}

// Report checks a lane report against the head commit. Every failure is one line.
func Report(text, head string) ReportResult {
	result := ReportResult{rows: map[section]int{}}
	walk := reportWalk{seen: map[section]bool{}, nones: map[section]int{}, tableRows: map[section]int{}}
	backgroundOK := false

	for _, line := range strings.Split(text, newline) {
		backgroundOK = backgroundOK || isNoBackgroundLine(line)
		result.Failures = append(result.Failures, walk.read(&result, line, head)...)
	}

	result.Failures = append(result.Failures, endFailures(walk.seen, result.rows, walk.nones)...)
	if !backgroundOK {
		result.Failures = append(result.Failures, `no line says "no background commands left". Stop every background command you started, by task id, then say so`)
	}

	return result
}

// reportWalk follows the walk over the lines of a report: the section it is in and what each section held.
type reportWalk struct {
	current          section
	seen             map[section]bool
	nones, tableRows map[section]int
}

func (w *reportWalk) read(result *ReportResult, line, head string) []string {
	if strings.HasPrefix(line, headingPrefix) {
		w.current = headingSection(line)
		w.seen[w.current] = w.seen[w.current] || w.current != ""
		w.tableRows[w.current] = 0

		return nil
	}

	if w.current == "" || !tableLine.MatchString(line) {
		return nil
	}

	w.tableRows[w.current]++
	if w.tableRows[w.current] <= headerRows {
		return nil
	}

	result.rows[w.current]++

	return rowFailures(w.current, line, head, result.rows[w.current], w.nones)
}

func headingSection(line string) section {
	name := section(strings.Trim(line[len(headingPrefix):], blanks))
	if columnsOf(name) > 0 {
		return name
	}

	return ""
}

func rowFailures(name section, line, head string, number int, nones map[section]int) []string {
	cells := splitRow(line)
	where := fmt.Sprintf("%s row %d", name, number)

	switch {
	case len(cells) > 0 && noneRow.MatchString(cells[0]):
		nones[name]++

		return nil
	case len(cells) != columnsOf(name):
		return []string{fmt.Sprintf("%s: %d columns, expected %d", where, len(cells), columnsOf(name))}
	}

	return cellFailures(name, cells, head, where)
}

func endFailures(seen map[section]bool, rows, nones map[section]int) []string {
	var failures []string

	for _, name := range reportSections() {
		switch {
		case !seen[name]:
			failures = append(failures, fmt.Sprintf("no ## %s section", name))
		case rows[name] == 0:
			failures = append(failures, fmt.Sprintf(`## %s has no rows. A table with nothing to list has one "none: <reason>" row`, name))
		case nones[name] > 0 && rows[name] > 1:
			failures = append(failures, fmt.Sprintf(`## %s mixes a "none:" row with other rows`, name))
		}
	}

	return failures
}
