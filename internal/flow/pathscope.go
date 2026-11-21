package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/workflow"
)

// scope is the set of path globs that an agent call may change. A scope with no globs allows every path.
type scope struct {
	patterns []string
	globs    []string
}

// readScope reads the allow_paths argument: a list of globs in the doublestar dialect. A `*` stops at a slash and a `**` crosses it.
// A missing argument gives no scope.
func readScope(v starlark.Value) (scope, error) {
	patterns, globs, err := workflow.AllowList("allow_paths", v)

	return scope{patterns: patterns, globs: globs}, err
}

func (s scope) active() bool { return len(s.globs) > 0 }

func (s scope) allows(name string) bool {
	return workflow.AllowsPath(s.globs, name)
}

// startMark is what an agent call with a scope records when it starts: the commit, and the untracked and ignored entries that were already
// in the folder. An untracked file carries its size and modification time. An ignored entry carries an empty value, and an ignored
// directory is one entry that ends with a slash, so a write inside a directory that was already ignored at the start is not seen.
//
// For a call that the engine commits, the mark also holds the state of each tracked file that differs from the commit: the hash of its content and its mode.
// The engine commits such a file only when its state is not the state at the start.
type startMark struct {
	Head  string            `json:"head"`
	Seen  map[string]string `json:"seen"`
	Dirty map[string]string `json:"dirty"`
}

// markStart records the start mark of an agent call with a scope or with a commit by the engine, in a step, so a replay keeps the first one.
// A call with neither records nothing.
func (s *script) markStart(a *agentArgs, name string) error {
	if !a.scope.active() && !a.commits() {
		return nil
	}

	started, err := runStep(s.e, s.ctx, name+"/start", func(ctx context.Context) (stepResult, error) {
		return markNote(ctx, a.git)
	})
	if err != nil {
		return err
	}

	a.start, err = parseMark(started.Note)

	return err
}

// parseMark reads the note that markNote wrote.
func parseMark(note string) (startMark, error) {
	var mark startMark
	if err := json.Unmarshal([]byte(note), &mark); err != nil {
		return startMark{}, fmt.Errorf("read the recorded start of the item folder: %w", err)
	}

	return mark, nil
}

// markNote takes the start mark now and puts it in the note of a step result.
func markNote(ctx context.Context, tree proc.Runner) (stepResult, error) {
	mark, err := takeMark(ctx, tree)
	if err != nil {
		return stepResult{}, err
	}

	note, err := json.Marshal(mark)

	return stepResult{Note: string(note)}, err
}

// takeMark reads the commit and the untracked and ignored entries of the folder now.
func takeMark(ctx context.Context, tree proc.Runner) (startMark, error) {
	head, err := folderHead(ctx, tree)
	if err != nil {
		return startMark{}, err
	}

	seen, err := others(ctx, tree)
	if err != nil {
		return startMark{}, err
	}

	dirty, err := dirtyTracked(ctx, tree)

	return startMark{Head: head, Seen: seen, Dirty: dirty}, err
}

// others maps each untracked or ignored entry of the folder to its stamp. The keys are relative to the root of the repository.
func others(ctx context.Context, tree proc.Runner) (map[string]string, error) {
	root, err := git(ctx, tree, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("find the root of the item folder: %w", err)
	}

	seen := map[string]string{}

	for _, ignored := range []bool{false, true} {
		names, listErr := listOthers(ctx, tree, ignored)
		if listErr != nil {
			return nil, listErr
		}

		for _, name := range names {
			seen[name] = stamp(strings.TrimSpace(root), name, ignored)
		}
	}

	return seen, nil
}

// stamp is the size and the modification time of an untracked file. A file that git lists and that is gone has the stamp `gone`.
// An ignored entry has an empty stamp.
func stamp(root, name string, ignored bool) string {
	if ignored {
		return ""
	}

	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "gone"
	}

	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// listOthers lists the untracked files, or the ignored entries with each ignored directory collapsed to one entry.
// The paths end with a NUL byte, so a name with a space or a non-ASCII letter stays whole.
func listOthers(ctx context.Context, tree proc.Runner, ignored bool) ([]string, error) {
	query := []string{"ls-files", "--others", "--exclude-standard", "--full-name", "-z"}
	if ignored {
		query = append(query, "--ignored", "--directory")
	}

	out, err := git(ctx, tree, append(query, "--", ":/")...)
	if err != nil {
		return nil, fmt.Errorf("list the changed paths of the item folder with git %s: %w", query[0], err)
	}

	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 }), nil
}

// changed lists the paths that the folder changed since the mark: every tracked change, committed or not, and every untracked or ignored
// entry that is new, gone or, for an untracked file, has another size or time. The diff runs with no rename detection, so a rename lists the old
// path as well as the new one. Every path is relative to the root of the repository.
func changed(ctx context.Context, tree proc.Runner, mark startMark) ([]string, error) {
	now, err := others(ctx, tree)
	if err != nil {
		return nil, err
	}

	out, err := git(ctx, tree, "diff", "--name-only", "--no-renames", "-z", mark.Head)
	if err != nil {
		return nil, fmt.Errorf("list the changed paths of the item folder with git diff: %w", err)
	}

	return append(slices.Collect(strings.SplitSeq(out, "\x00")), differ(mark.Seen, now)...), nil
}

// differ lists the entries that are new, gone or have another stamp between two snapshots.
func differ(before, now map[string]string) []string {
	var names []string

	for name, was := range now {
		if stamp, ok := before[name]; !ok || stamp != was {
			names = append(names, name)
		}
	}

	for name := range before {
		if _, ok := now[name]; !ok {
			names = append(names, name)
		}
	}

	return names
}

// outside lists the changed paths that no glob allows.
func (s scope) outside(ctx context.Context, tree proc.Runner, mark startMark) ([]string, error) {
	names, err := changed(ctx, tree, mark)
	if err != nil {
		return nil, err
	}

	return s.blocked(names), nil
}

// blocked lists the names that no glob allows, sorted and with no repeat.
func (s scope) blocked(names []string) []string {
	var bad []string

	for _, name := range names {
		if name != "" && !s.allows(name) && !slices.Contains(bad, name) {
			bad = append(bad, name)
		}
	}

	slices.Sort(bad)

	return bad
}

// check fails when the folder changed a path that no glob allows. The message names each path.
func (s scope) check(ctx context.Context, tree proc.Runner, mark startMark) error {
	if !s.active() {
		return nil
	}

	bad, err := s.outside(ctx, tree, mark)
	if err != nil {
		return err
	}

	if len(bad) > 0 {
		return fmt.Errorf("the agent changed %d path(s) outside allow_paths [%s]: %s", len(bad), strings.Join(s.patterns, ", "), strings.Join(bad, ", "))
	}

	return nil
}
