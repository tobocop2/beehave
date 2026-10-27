// Package audit reads the merge commits of a round for content that a pull request diff never shows.
//
// Authored sweeps every merge commit for the paths whose blob in the merge differs from every parent. Present asks, for each merge, whether the lines it added are still at the tip.
package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const (
	subjectWidth     = 58
	exitNoWorktree   = 9
	exitGitMissing   = 127
	pullRequestMerge = "Merge pull request #"
	absent           = "ABSENT"
	sensitiveMark    = "  <== sensitive path"
)

// ErrNoGit says that git is not installed.
var ErrNoGit = errors.New("git is not installed")

// NoWorktree says that the checkout to audit cannot be entered.
type NoWorktree struct{ Path string }

func (e NoWorktree) Error() string { return "cannot enter " + e.Path }

// Code is the exit code of a run that ends on this error.
func (NoWorktree) Code() int { return exitNoWorktree }

// Line is one line of an audit report. It can hold more than one text line.
type Line string

func (l Line) String() string { return string(l) }

type runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// Audit reads one checkout through git.
type Audit struct{ git runner }

// New returns an audit that runs git through the runner.
func New(git runner) *Audit { return &Audit{git: git} }

// Open returns an audit of the folder. A folder that is not there is a NoWorktree error.
func Open(git runner, worktree string) (*Audit, error) {
	if info, err := os.Stat(worktree); err != nil || !info.IsDir() {
		return nil, NoWorktree{Path: worktree}
	}

	return New(git), nil
}

// IsSensitive is true for a path whose change in a merge needs a human to read it.
func IsSensitive(file string) bool {
	for _, dir := range []string{".github/", "scripts/", "sql/"} {
		if strings.HasPrefix(file, dir) {
			return true
		}
	}

	name := path.Base(file)

	for _, pattern := range []string{"*baseline*", "*allowlist*", "*allow-list*", "*.lock", "*lockfile*", "*pin*", "*gate*", "*.secrets*", "*censu*"} {
		if ok, _ := path.Match(pattern, name); ok { //nolint:errcheck // the patterns are constants and well formed
			return true
		}
	}

	return false
}

// reader runs the git calls of one report. The first failure stays in err and every later call returns nothing, so a caller checks err once at the end.
type reader struct {
	ctx context.Context //nolint:containedctx // a reader lives for one report call
	git runner
	err error
}

// try runs git and reports whether it exited 0. A missing git is the only error.
func (r *reader) try(args ...string) (bool, string) {
	if r.err != nil {
		return false, ""
	}

	result := r.git.Run(r.ctx, args)
	if result.ExitCode == exitGitMissing {
		r.err = ErrNoGit

		return false, ""
	}

	return result.ExitCode == 0, result.Stdout
}

// read runs a git call that must succeed and returns what it printed.
func (r *reader) read(args ...string) string {
	ok, out := r.try(args...)
	if !ok && r.err == nil {
		r.err = fmt.Errorf("git %s failed", strings.Join(args, " "))
	}

	return out
}

// split returns the non-empty parts of the output of a call that must succeed. A -z listing splits on NUL.
func (r *reader) split(sep string, args ...string) []string {
	var kept []string

	for _, part := range strings.Split(r.read(args...), sep) {
		if part != "" {
			kept = append(kept, part)
		}
	}

	return kept
}

func (r *reader) lines(args ...string) []string { return r.split("\n", args...) }

func (r *reader) paths(args ...string) []string { return r.split("\x00", args...) }

// one returns the single value that git prints, without the space around it.
func (r *reader) one(args ...string) string { return strings.TrimSpace(r.read(args...)) }

// subject returns the first line of the subject of a commit.
func (r *reader) subject(commit string) string {
	first, _, _ := strings.Cut(r.read("log", "--format=%s", "-1", commit), "\n")

	return first
}

func (r *reader) blob(commit, file string) string {
	if ok, out := r.try("rev-parse", commit+":"+file); ok {
		return strings.TrimSpace(out)
	}

	return absent
}

func (r *reader) merges(span string) []string { return r.lines("rev-list", "--merges", span) }

// Authored prints the authored and resolved paths of every merge in the range.
func (a *Audit) Authored(ctx context.Context, span string) ([]Line, error) {
	r := &reader{ctx: ctx, git: a.git}
	total := 0

	var out []Line

	for _, merge := range r.merges(span) {
		entry, found := r.authoredMerge(merge)
		total += found

		if entry != "" {
			out = append(out, Line(entry))
		}
	}

	if r.err != nil {
		return nil, r.err
	}

	return append(out, Line(fmt.Sprintf("TOTAL_AUTHORED_PATHS=%d", total)), "AUD_AUTHORED_DONE"), nil
}

// authoredMerge returns the report of one merge, empty when it differs from no parent, and its count of authored paths.
func (r *reader) authoredMerge(merge string) (string, int) {
	label := r.one("log", "--format=%h", "-1", merge)
	subject := r.subject(merge)
	count, resolved, report := r.authoredPaths(merge)

	if len(report) == 0 && count == 0 {
		return "", 0
	}

	head := fmt.Sprintf("%s differs_from_all_parents=%d resolved=%d AUTHORED=%d | %s", label, count, resolved, len(report), cut(subject))

	return strings.Join(append([]string{head}, report...), "\n"), len(report)
}

// cut keeps the first subjectWidth characters of a subject.
func cut(subject string) string {
	runes := []rune(subject)
	if len(runes) > subjectWidth {
		return string(runes[:subjectWidth])
	}

	return subject
}

// authoredPaths returns the paths of a merge that differ from every parent: the total, the resolved count and the report of the authored ones.
func (r *reader) authoredPaths(merge string) (int, int, []string) {
	first, second := r.one("rev-parse", merge+"^1"), r.one("rev-parse", merge+"^2")
	// -z keeps a non-ASCII path as it is. Without it git quotes the name and the blob lookup fails.
	listed := r.paths("diff-tree", "-z", "-c", "-r", "--name-only", "--no-commit-id", merge)
	resolved := 0

	var report []string

	for _, file := range listed {
		before, after := r.blob(first, file), r.blob(second, file)
		if before != after {
			resolved++

			continue
		}

		report = append(report, authoredLine(before == absent, file))
	}

	return len(listed), resolved, report
}

func authoredLine(added bool, file string) string {
	how := "identical in both parents, changed by the merge"
	if added {
		how = "in neither parent, added by the merge"
	}

	mark := ""
	if IsSensitive(file) {
		mark = sensitiveMark
	}

	return fmt.Sprintf("      AUTHORED (%s): %s%s", how, file, mark)
}

// Present prints, for each merge from the base to the tip, which added lines the tip no longer has.
func (a *Audit) Present(ctx context.Context, tip, base string) ([]Line, error) {
	r := &reader{ctx: ctx, git: a.git}
	resolved := r.one("rev-parse", tip)
	merges := r.merges(base + ".." + tip)
	out := []Line{
		Line("AUDIT_TIP=" + resolved),
		Line(fmt.Sprintf("MERGES_ENUMERATED=%d (from the branch, not a fixed list)", len(merges))),
	}

	for _, merge := range merges {
		out = append(out, Line(r.presentMerge(tip, r.mergeLabel(merge), merge)))
	}

	if r.err != nil {
		return nil, r.err
	}

	return append(out, "AUD19_PRESENT_DONE"), nil
}

// mergeLabel is the number of a pull request merge, or the short hash of any other merge with an m before it.
func (r *reader) mergeLabel(merge string) string {
	if number, ok := strings.CutPrefix(r.subject(merge), pullRequestMerge); ok {
		label, _, _ := strings.Cut(number, " ")

		return label
	}

	return "m" + r.one("log", "--format=%h", "-1", merge)
}

// presentMerge reports one merge: the files it changed, how many the branch touched since and which added lines are gone.
func (r *reader) presentMerge(tip, label, merge string) string {
	if ok, _ := r.try("merge-base", "--is-ancestor", merge, tip); !ok && r.err == nil {
		return fmt.Sprintf("#%s MERGE_NOT_ANCESTOR_OF_TIP %s  <-- FINDING: the merge left the branch", label, merge)
	}

	parent := r.one("rev-parse", merge+"^1")
	files := r.paths("diff", "--name-only", "-z", parent, merge)
	since := r.since(tip, parent, merge, files)

	return fmt.Sprintf("#%s %s files=%d touched_since=%d lines_gone_in_surviving_files=%d files_deleted=%d%s",
		label, merge, len(files), since.touched, since.gone, since.deleted, since.detail)
}

// tally counts what the branch did to the files of one merge after it.
type tally struct {
	touched, gone, deleted int
	detail                 string
}

// since checks each file of the merge that a later commit changed.
func (r *reader) since(tip, parent, merge string, files []string) tally {
	var t tally

	for _, file := range files {
		if len(r.lines("log", "--oneline", merge+".."+tip, "--", file)) == 0 {
			continue
		}

		t.touched++

		lost, line := r.lost(tip, parent, merge, file)
		t.detail += line

		if lost > 0 {
			t.gone += lost
		} else if lost < 0 {
			t.deleted++
		}
	}

	return t
}

// addedLines returns the lines the merge added to one path, without the file header lines.
func (r *reader) addedLines(parent, merge, file string) []string {
	var added []string

	for _, line := range strings.Split(r.read("diff", parent, merge, "--", file), "\n") {
		if len(line) > 1 && line[0] == '+' && line[1] != '+' {
			added = append(added, line[1:])
		}
	}

	return added
}

// countMissing counts the added lines that the text of the file at the tip does not hold. A file that cannot be shown holds only the empty line.
func countMissing(added []string, shown bool, text string) int {
	present := map[string]bool{"": true}

	if shown {
		present = map[string]bool{}

		for _, line := range strings.Split(text, "\n") {
			present[line] = true
		}
	}

	missing := 0

	for _, line := range added {
		if !present[line] {
			missing++
		}
	}

	return missing
}

// lost returns how many added lines of a path are gone at the tip, and the line that says so. A count of -1 says the file is gone.
func (r *reader) lost(tip, parent, merge, file string) (int, string) {
	added := r.addedLines(parent, merge, file)
	if len(added) == 0 {
		return 0, ""
	}

	shown, text := r.try("show", tip+":"+file)
	missing := countMissing(added, shown, text)

	if missing == 0 {
		return 0, ""
	}

	if exists, _ := r.try("cat-file", "-e", tip+":"+file); exists {
		return missing, fmt.Sprintf("\n    SIGNAL: %d of %d added lines gone, file still present: %s", missing, len(added), file)
	}

	return -1, fmt.Sprintf("\n    file deleted at tip (check it was a deliberate retirement): %s (%d lines)", file, len(added))
}
