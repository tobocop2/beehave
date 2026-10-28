package brief

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	whyOpen  = "<!--why"
	whyClose = "why-->"
	forOpen  = "<!-- for:"
	forClose = "-->"

	oneLineWhy = "text follows why--> on the same line. Put why--> at the end of a line, with nothing after it."
)

var (
	headingLine  = regexp.MustCompile(`^(#+) `)
	fenceLine    = regexp.MustCompile("^(```|~~~)")
	whyOpenLoose = regexp.MustCompile(`^[ \t\n\v\f\r]*<!--[ \t\n\v\f\r]*why`)
	forOpenLoose = regexp.MustCompile(`^[ \t\n\v\f\r]*<!--[ \t\n\v\f\r]*for`)
	whyEnd       = regexp.MustCompile(`why-->[ \t\n\v\f\r]*$`)
)

// Lean returns the rules of a project as a lane of kind reads them.
//
// A `<!--why` block is dropped. A first line `<!-- for: impl rev -->` under a heading keeps the section for the named kinds, until the next heading of the same or a higher level.
// A malformed marker is a refusal with the file and the line.
func Lean(text string, kind Kind, filename string) (string, error) {
	cut := &leaner{kind: kind, filename: filename}

	return cut.run(text)
}

// leaner holds the state of one cut: one method for each marker.
type leaner struct {
	kind      Kind
	filename  string
	out       []string
	inWhy     bool
	whyLine   int
	fence     string
	held      string
	skip      bool
	skipLevel int
	level     int
	seen      bool
	afterHead bool
}

func (l *leaner) die(number int, message string) error {
	return refuse("%s:%d: %s", l.filename, number, message)
}

// flush puts the held heading in the output unless a cut drops it.
func (l *leaner) flush() {
	if l.held != "" && !l.skip {
		l.out = append(l.out, l.held)
	}

	l.held = ""
}

func (l *leaner) run(text string) (string, error) {
	for i, line := range awkLines(text) {
		if err := l.feed(i+1, line); err != nil {
			return "", err
		}
	}

	if l.inWhy {
		return "", l.die(l.whyLine, "the <!--why block opened here has no why--> line.")
	}

	l.flush()

	return strings.TrimRight(strings.Join(l.out, "\n"), "\n") + "\n", nil
}

func (l *leaner) feed(number int, line string) error {
	switch {
	case l.inWhy:
		return l.insideWhy(number, line)
	case strings.HasPrefix(line, whyOpen):
		return l.openWhy(number, line)
	}

	if l.fence == "" {
		handled, err := l.marker(number, line)
		if err != nil || handled {
			return err
		}
	}

	l.plain(line)

	return nil
}

func (l *leaner) insideWhy(number int, line string) error {
	if whyOpenLoose.MatchString(line) {
		return l.die(number, "a <!--why block opens inside the block opened at line "+strconv.Itoa(l.whyLine)+". Close it with why--> first.")
	}

	if strings.Contains(line, whyClose) {
		if !whyEnd.MatchString(line) {
			return l.die(number, oneLineWhy)
		}

		l.inWhy = false
	}

	return nil
}

func (l *leaner) openWhy(number int, line string) error {
	l.inWhy = !strings.Contains(line, whyClose)
	l.whyLine = number

	if !l.inWhy && !whyEnd.MatchString(line) {
		return l.die(number, oneLineWhy)
	}

	return nil
}

// marker handles a line outside a fence that is a heading or a for line, and refuses a typo of either. It reports whether the line was one.
func (l *leaner) marker(number int, line string) (bool, error) {
	if err := l.typo(number, line); err != nil {
		return false, err
	}

	if heading := headingLine.FindStringSubmatch(line); heading != nil {
		l.heading(line, len(heading[1]))

		return true, nil
	}

	if strings.HasPrefix(line, forOpen) {
		return true, l.forLine(number, line)
	}

	return false, nil
}

// typo refuses a line that looks like a why marker or a for marker and is not one.
func (l *leaner) typo(number int, line string) error {
	if whyOpenLoose.MatchString(line) {
		return l.die(number, "this line looks like a why marker but is not one. "+
			"A why block opens with <!--why at the start of the line, with no indent and no space.")
	}

	if forOpenLoose.MatchString(line) && !strings.HasPrefix(line, forOpen) {
		return l.die(number, "this line looks like a for: marker but is not one. "+
			"The form is <!-- for: KIND ... --> at the start of the line, with one space after <!-- and a colon after for.")
	}

	return nil
}

func (l *leaner) heading(line string, level int) {
	l.flush()
	l.level = level

	if l.skip && level <= l.skipLevel {
		l.skip = false
	}

	l.held = line
	l.seen = true
	l.afterHead = true
}

// placed refuses a for line that has no closing arrow, or that is not the first line under a heading.
func (l *leaner) placed(number int, line string) error {
	switch {
	case !strings.Contains(line, forClose):
		return l.die(number, "a for: line has no closing -->.")
	case !l.seen:
		return l.die(number, "a for: line comes before any heading. It must be the first line under one.")
	case !l.afterHead:
		return l.die(number, "a for: line must be the first line under its heading, with no blank line or text between them.")
	}

	return nil
}

func (l *leaner) forLine(number int, line string) error {
	if err := l.placed(number, line); err != nil {
		return err
	}

	spec := strings.TrimLeft(strings.TrimPrefix(line, forOpen), " ")
	spec, _, _ = strings.Cut(spec, forClose)

	kept, err := l.kindsKept(number, strings.FieldsFunc(spec, notWord))
	if err != nil {
		return err
	}

	l.cut(kept)

	return nil
}

// cut starts a skip when the section is not for the lane's kind, and drops the held heading while a skip lasts.
func (l *leaner) cut(kept bool) {
	l.afterHead = false

	if !kept && !l.skip {
		l.skip = true
		l.skipLevel = l.level
	}

	if l.skip {
		l.held = ""
	}
}

// notWord is true for the characters that end a kind name in a for line.
func notWord(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n'
}

// kindsKept reports whether the lane's kind is among the names, and refuses a line with no name or an unknown one.
func (l *leaner) kindsKept(number int, names []string) (bool, error) {
	if len(names) == 0 {
		return false, l.die(number, "a for: line names no kind. The kinds are "+kindList+".")
	}

	kept := false

	for _, name := range names {
		kind, err := ParseKind(name)
		if err != nil {
			return false, l.die(number, `a for: line names the kind "`+name+`". The kinds are `+kindList+".")
		}

		kept = kept || kind == l.kind
	}

	return kept, nil
}

func (l *leaner) plain(line string) {
	if open := fenceLine.FindString(line); open != "" {
		switch l.fence {
		case "":
			l.fence = open
		case open:
			l.fence = ""
		}
	}

	l.flush()
	l.afterHead = false

	if !l.skip {
		l.out = append(l.out, line)
	}
}
