package brief

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	readFirstHeading = "## Read first"
	expectedLabel    = "Expected behavior"
	expectedHeading  = sectionPrefix + expectedLabel
)

var (
	listItem   = regexp.MustCompile(`^[-*] `)
	itemMarker = regexp.MustCompile(`^[-*] +`)
)

// Check sizes the required reading of a filled brief. It warns about big files, a chain of briefs and an expected-result line that is missing,
// and refuses a brief whose required reading is above ReadLimit. The report holds what was found, also when the brief is refused.
func Check(name string, opts SizeOptions) (Report, error) {
	if !isFile(name) {
		return Report{}, refuse("no brief at %s. Write the brief first, or give the path of the one you wrote.", name)
	}

	text, err := ReadText(name)
	if err != nil {
		return Report{}, err
	}

	report := &Report{}
	err = checkText(report, name, text, opts)

	return *report, err
}

// checkText adds to the report what the text of the brief shows, and refuses a reading above the limit.
func checkText(report *Report, name, text string, opts SizeOptions) error {
	other, err := sizeListed(report, listUnderReadFirst(text))
	if err != nil {
		return err
	}

	warnSize(report, name, opts)

	if err := warnExpected(report, name, text); err != nil {
		return err
	}

	if err := SizeGate(len(text), other, ReadLimit); err != nil {
		return err
	}

	report.say("OK: the required reading of %s is %d bytes, at most %d.", name, len(text)+other, ReadLimit)

	return nil
}

// sizeListed returns the bytes of the listed files and warns about each file above FileWarn bytes.
func sizeListed(report *Report, listed []string) (int, error) {
	total := 0

	for _, path := range listed {
		size, err := ReadBytes([]string{path})
		if err != nil {
			return 0, err
		}

		total += size

		if size > FileWarn {
			report.warn(fmt.Sprintf("WARN: %s is %d bytes, above %d. List a digest of it, or the section the lane needs, under '%s'.",
				path, size, FileWarn, readFirstHeading))
		}
	}

	return total, nil
}

// warnSize adds the warnings of the size report. A file that the size report cannot read is a warning here, because the check refuses only for size.
func warnSize(report *Report, name string, opts SizeOptions) {
	sized, err := Size(name, opts)
	report.warn(sized.Warn...)

	if err != nil {
		report.warn(err.Error())
	}
}

// listUnderReadFirst returns the first word of each list item under the read-first heading, backticks removed.
func listUnderReadFirst(text string) []string {
	var paths []string

	inside := false

	for _, line := range awkLines(text) {
		switch {
		case strings.HasPrefix(line, sectionPrefix):
			inside = strings.HasPrefix(line, readFirstHeading)
		case inside && listItem.MatchString(line):
			word := strings.ReplaceAll(itemMarker.ReplaceAllString(line, ""), "`", "")
			if word, _, _ = strings.Cut(word, " "); word != "" {
				paths = append(paths, word)
			}
		}
	}

	return paths
}

// firstValue returns the value after the first `- <label>: ` line, backticks removed.
func firstValue(text, label string) string {
	prefix := "- " + label + ": "

	for _, line := range awkLines(text) {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.ReplaceAll(strings.TrimLeft(rest, " "), "`", "")
		}
	}

	return ""
}

// expectedProblem says what is wrong with the expected-result line of a brief that names an acceptance script, or returns an empty string.
func expectedProblem(text string) (string, error) {
	acceptance := firstValue(text, "Acceptance script")
	if acceptance == "" || strings.HasPrefix(acceptance, noReason) {
		return "", nil
	}

	return expectedWhy(firstValue(text, expectedLabel))
}

// expectedWhy says what is wrong with the value of the expected-behavior line, or returns an empty string.
func expectedWhy(expected string) (string, error) {
	switch {
	case strings.HasPrefix(expected, slotPrefix):
		return "", nil
	case expected == "" || strings.HasPrefix(expected, "[") || strings.HasPrefix(expected, "<"):
		return "names an acceptance script and no expected behavior", nil
	case strings.HasPrefix(expected, noReason):
		return noneWhy(expected), nil
	case strings.HasPrefix(expected, "/") && isFile(expected):
		return headingProblem(expected)
	}

	return "", nil
}

// noneWhy refuses a `none:` with no reason after it.
func noneWhy(expected string) string {
	if strings.Trim(strings.TrimPrefix(expected, noReason), blanks) == "" {
		return "says 'none:' for the expected behavior, with no reason"
	}

	return ""
}

// headingProblem says what is wrong with an expected-behavior file that has no heading.
func headingProblem(path string) (string, error) {
	text, err := ReadText(path)
	if err != nil || hasLine(awkLines(text), expectedHeading) {
		return "", err
	}

	return "names " + path + " for the expected behavior, and that file has no '" + expectedHeading + "' heading", nil
}

// warnExpected warns when the brief names an acceptance script and its expected behavior is missing or hollow.
func warnExpected(report *Report, name, text string) error {
	why, err := expectedProblem(text)
	if err != nil || why == "" {
		return err
	}

	report.warn(
		"WARN: "+name+" "+why+".",
		"      When several outputs, formats, routes or settings render the changed value by different rules, write",
		"      one row for each input, output and setting, with the value at the base and the expected value, under",
		"      '"+expectedHeading+"' in <round folder>/rulings/<item>.md. Then write '- Expected behavior: <that file>'",
		"      in the brief. When one rule renders the value in every output, write '- Expected behavior: none: <reason>'.",
	)

	return nil
}
