package brief

import (
	"regexp"
	"strings"
)

const (
	preflightHeading = "## Maintainer preflight"
	ciHeading        = "## CI checks and their base"
	standingHeading  = "## Standing rules"
	takePrefix       = "- Take:"
	takeNone         = "none"
	sectionPrefix    = "## "
)

var (
	ciRunLine  = regexp.MustCompile(`^- Run: *[^ ]`)
	ciBaseLine = regexp.MustCompile(`^- Base: *[^ ]`)
)

// Rules is the rules file of a project: where it is, what it says and the standing blocks it takes.
type Rules struct {
	Path  string
	Text  string
	Taken Taken
}

// LoadRules reads the rules file at path and refuses it when a required section is missing.
// The file needs a preflight section, and a CI section with a `- Run:` line and a `- Base:` line.
func LoadRules(path string) (Rules, error) {
	text, err := readRules(path)
	if err != nil {
		return Rules{}, err
	}

	lines := awkLines(text)

	if err := requireSections(lines, path); err != nil {
		return Rules{}, err
	}

	taken, err := standing(lines, path)
	if err != nil {
		return Rules{}, err
	}

	return Rules{Path: path, Text: text, Taken: taken}, nil
}

func readRules(path string) (string, error) {
	if !isFile(path) {
		return "", refuse("no brief rules at %s. Put the rules of the project in that file.", path)
	}

	return ReadText(path)
}

// requireSections refuses a rules file with no preflight section, or with no CI section that holds a run line and a base line.
func requireSections(lines []string, path string) error {
	if !hasLine(lines, preflightHeading) {
		return refuse("%s has no '%s' section. Put the review findings that repeat on this repository there, "+
			"or write 'none measured yet' under the heading.", path, preflightHeading)
	}

	if !hasCISection(lines) {
		return refuse("%s has no '%s' section with a '- Run:' line and a '- Base:' line. "+
			"Add the section: under it, one '- Run: <command>' line for each check the repository's CI runs on a pull request "+
			"(format, lint, quality), and one '- Base: <the ref those checks compare with, and how to pass it>' line. "+
			"Read them from the CI workflow file.", path, ciHeading)
	}

	return nil
}

// Lean returns the rules as a lane of kind reads them.
func (r Rules) Lean(kind Kind) (string, error) {
	return Lean(r.Text, kind, r.Path)
}

func hasLine(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

// hasCISection reports whether the CI section holds a run line and a base line.
func hasCISection(lines []string) bool {
	inside, hasRun, hasBase := false, false, false

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, sectionPrefix):
			inside = strings.HasPrefix(line, ciHeading)
		case inside:
			hasRun = hasRun || ciRunLine.MatchString(line)
			hasBase = hasBase || ciBaseLine.MatchString(line)
		}
	}

	return hasRun && hasBase
}

// standing returns the blocks named by the first `- Take:` line of the standing section. A file with no such line takes every block.
func standing(lines []string, path string) (Taken, error) {
	take, found := takeLine(lines)
	if !found {
		return Taken(Blocks()), nil
	}

	if take == takeNone {
		return Taken{}, nil
	}

	if take == "" {
		return nil, refuse("%s has '%s' with no block under '%s'. Name the blocks: build-host, compiled, union, or none.",
			path, takePrefix, standingHeading)
	}

	return parseTake(take, path)
}

// parseTake returns the blocks that the words of a take line name, and refuses a word that names no block.
func parseTake(take, path string) (Taken, error) {
	taken := Taken{}

	for _, name := range strings.FieldsFunc(take, isBlank) {
		block, err := ParseBlock(name)
		if err != nil {
			return nil, refuse("%s takes the standing block '%s', which does not exist. "+
				"The blocks are build-host, compiled and union.", path, name)
		}

		taken = append(taken, block)
	}

	return taken, nil
}

// takeLine returns the text after the first `- Take:` line under the standing heading.
func takeLine(lines []string) (string, bool) {
	inside := false

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, sectionPrefix):
			inside = strings.HasPrefix(line, standingHeading)
		case inside && strings.HasPrefix(line, takePrefix):
			return strings.Trim(strings.TrimPrefix(line, takePrefix), blanks), true
		}
	}

	return "", false
}

func isBlank(r rune) bool {
	return strings.ContainsRune(blanks, r)
}
