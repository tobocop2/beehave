package brief

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// FileWarn is the size in bytes above which a file under the read-first list draws a warning.
	FileWarn = 8000
	// FreshLaneTokens is what a lane has read before its first step: the tools, the system text and the rules.
	FreshLaneTokens = 35000

	rootMarker  = "`<S>` is `"
	briefsDir   = "/briefs/"
	unknownRoot = "<S> is unknown: pass the round folder as the root. A <S>/ path is unsized."
)

// Report holds the lines a command shows: Out for the answer and Warn for the warnings.
type Report struct {
	Out  []string
	Warn []string
}

func (r *Report) say(format string, args ...any) {
	r.Out = append(r.Out, fmt.Sprintf(format, args...))
}

func (r *Report) warn(lines ...string) {
	r.Warn = append(r.Warn, lines...)
}

// SizeOptions say how to size a brief. Root is the round folder when the caller names it; Fallback is the folder to use when
// neither Root nor the brief names one. Cap is the lane context cap in tokens, and zero means ContextCap.
type SizeOptions struct {
	Root     string
	Fallback string
	Cap      int
}

// Size reports what a brief makes a lane read: the brief, each file it names, and the briefs those name.
// It warns and never refuses a brief for its size.
func Size(name string, opts SizeOptions) (Report, error) {
	if !isFile(name) {
		return Report{}, refuse("no brief at %s. Write the brief first, or give the path of the one you wrote.", name)
	}

	tokens := cmp.Or(opts.Cap, ContextCap)
	if err := CheckCap(tokens); err != nil {
		return Report{}, err
	}

	brief := filepath.Clean(name)

	text, err := ReadText(brief)
	if err != nil {
		return Report{}, err
	}

	report := &Report{}
	err = sizeText(report, brief, text, opts, tokens)

	return *report, err
}

func sizeText(report *Report, brief, text string, opts SizeOptions, tokens int) error {
	root := roundRoot(opts, text)
	report.say("brief-size: %s", brief)
	report.say("%s", rootLine(root))
	report.say("%8s %9s  file", "tokens", "bytes")

	walk := newWalker(report, brief, text, root)
	walk.row(len(text), brief+"  (the brief)")

	if err := walk.run(); err != nil {
		return err
	}

	walk.found.say(report, brief, tokens)

	return nil
}

func newWalker(report *Report, brief, text, root string) *walker {
	return &walker{
		found:  scan{total: len(text), files: 1},
		seen:   map[string]bool{brief: true},
		queue:  []named{{path: brief, text: text}},
		report: report,
		root:   root,
	}
}

func rootLine(root string) string {
	if root == "" {
		return unknownRoot
	}

	return "<S> is " + root
}

// roundRoot returns the round folder: the one named, else the one the brief states about itself, else the fallback.
func roundRoot(opts SizeOptions, text string) string {
	root := cmp.Or(opts.Root, rootOf(text), opts.Fallback)

	return strings.TrimSuffix(root, "/")
}

// rootOf returns the folder a brief states about itself: a line with "`<S>` is `<path>`". A path that is not absolute is no folder.
func rootOf(text string) string {
	for _, line := range awkLines(text) {
		at := strings.Index(line, rootMarker)
		if at < 0 {
			continue
		}

		rest := line[at+len(rootMarker):]
		if end := strings.Index(rest, "`"); end > 0 {
			if strings.HasPrefix(rest, "/") {
				return rest[:end]
			}

			return ""
		}
	}

	return ""
}

// scan is the bytes a brief makes a lane read, and the briefs it names on the way.
type scan struct {
	total   int
	files   int
	unsized int
	chain   []string
}

// named is a brief that was found on the way, with its text, waiting for the files it names to be sized.
type named struct {
	path string
	text string
}

// walker sizes the brief and each file it names, following the briefs it names, each file once.
type walker struct {
	found  scan
	seen   map[string]bool
	queue  []named
	report *Report
	root   string
}

func (w *walker) run() error {
	for len(w.queue) > 0 {
		current := w.queue[0]
		w.queue = w.queue[1:]

		if err := w.follow(current); err != nil {
			return err
		}
	}

	return nil
}

// follow sizes each path that a brief names and that the walk has not seen.
func (w *walker) follow(current named) error {
	for _, path := range namesIn(current.text, w.root) {
		if w.seen[path] {
			continue
		}

		w.seen[path] = true

		if err := w.size(path, filepath.Base(current.path)); err != nil {
			return err
		}
	}

	return nil
}

func (w *walker) row(size int, label string) {
	w.report.say("%8s %9s  %s", strconv.Itoa(size/BytesPerToken), strconv.Itoa(size), label)
}

// size adds the file at path to the total. A path that is no file is unsized, and a folder is skipped.
func (w *walker) size(path, namedBy string) error {
	if !isFile(path) {
		w.skip(path)

		return nil
	}

	text, err := ReadText(path)
	if err != nil {
		return err
	}

	w.found.total += len(text)
	w.found.files++
	w.row(len(text), w.label(path, namedBy, text))

	return nil
}

// label returns the name of a file in the report. A file under a briefs folder is a brief: the walk follows its names too.
func (w *walker) label(path, namedBy, text string) string {
	if !strings.Contains(path, briefsDir) {
		return path
	}

	w.found.chain = append(w.found.chain, path)
	w.queue = append(w.queue, named{path: path, text: text})

	return path + "  (UNDER briefs/, named by " + namedBy + "; its names are counted below)"
}

func (w *walker) skip(path string) {
	if isDir(path) {
		return
	}

	w.found.unsized++
	w.report.say("%8s %9s  %s", "-", "-", strings.TrimPrefix(path, "?")+"  (unsized: not a file here)")
}

// say adds the total and the budget to the report, with the warnings for a chain of briefs and a reading above the limit.
func (s scan) say(report *Report, brief string, tokens int) {
	report.say("TOTAL: about %d tokens, %d bytes, in %d files; %d named paths unsized.", s.total/BytesPerToken, s.total, s.files, s.unsized)
	report.say("BUDGET: %d tokens (the read limit is %d bytes). The lane context cap is %d tokens, and a fresh lane starts at about 35,000.",
		ReadLimit/BytesPerToken, ReadLimit, tokens)

	s.warnChain(report, brief)

	if s.total > ReadLimit {
		report.warn(
			fmt.Sprintf("WARN: the named reading of %s is about %d tokens (%d bytes), above the budget of", brief, s.total/BytesPerToken, s.total),
			fmt.Sprintf("      %d tokens (the read limit is %d bytes). With a fresh start of about 35,000 tokens, the lane is at", ReadLimit/BytesPerToken, ReadLimit),
			fmt.Sprintf("      about %d of its %d tokens before its first step. Cut the list.", s.total/BytesPerToken+FreshLaneTokens, tokens),
		)
	}
}

func (s scan) warnChain(report *Report, brief string) {
	for _, path := range s.chain {
		report.warn("WARN: the reading of " + brief + " names a file under a briefs/ directory: " + path)
	}

	if len(s.chain) > 0 {
		report.warn(
			"      A replacement brief is self-contained. It restates the rulings and duties that bind, and it names",
			"      the rules files, the handoff, the diff and the job script. Copy what binds from an earlier brief",
			"      into <round folder>/rulings/<item>.md one time, and name that file in place of the brief.",
		)
	}
}
