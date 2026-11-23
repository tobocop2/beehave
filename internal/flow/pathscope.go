package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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

// startMark is what an agent call with a scope records when it starts: the commit, the untracked and ignored files that were already in the
// folder, and the files beside the folder. Each file carries its size and modification time. A folder of a known build cache is one entry that
// ends with a slash and has an empty value. A mark with no Outside map was recorded before the files beside the folder were read, so those are not checked.
//
// For a call that the engine commits, the mark also holds the state of each tracked file that differs from the commit: the hash of its content and its mode.
// The engine commits such a file only when its state is not the state at the start.
type startMark struct {
	Head    string            `json:"head"`
	Seen    map[string]string `json:"seen"`
	Dirty   map[string]string `json:"dirty"`
	Outside map[string]string `json:"outside"`
}

// isBuildCache says whether a folder name is one that tools fill by themselves: a write inside one is not a change of the step, so each one is a single entry.
func isBuildCache(name string) bool {
	switch name {
	case ".cache", ".gradle", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".venv", "__pycache__", "node_modules":
		return true
	}

	return false
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

	now, err := readState(ctx, tree)
	if err != nil {
		return startMark{}, err
	}

	dirty, err := dirtyTracked(ctx, tree)

	return startMark{Head: head, Seen: now.seen, Dirty: dirty, Outside: now.outside}, err
}

// folderState is the files that a scoped call watches: the untracked and ignored files of the repository, and the files beside the item folder.
type folderState struct {
	seen    map[string]string
	outside map[string]string
}

// readState reads the files of the repository that git does not track, and the files beside the item folder, with their stamps.
// The keys of seen are relative to the root of the repository; the keys of outside are relative to the root and start with `../`.
func readState(ctx context.Context, tree proc.Runner) (folderState, error) {
	out, err := git(ctx, tree, "rev-parse", "--show-toplevel")
	if err != nil {
		return folderState{}, fmt.Errorf("find the root of the item folder: %w", err)
	}

	root := strings.TrimSpace(out)

	seen, err := untrackedStamps(ctx, tree, root)
	if err != nil {
		return folderState{}, err
	}

	outside, err := besideFolder(ctx, tree, root)

	return folderState{seen: seen, outside: outside}, err
}

// untrackedStamps maps each untracked file, and each ignored file or build cache folder, of the repository at root to its stamp.
func untrackedStamps(ctx context.Context, tree proc.Runner, root string) (map[string]string, error) {
	seen := map[string]string{}

	for _, ignored := range []bool{false, true} {
		names, err := listOthers(ctx, tree, ignored)
		if err != nil {
			return nil, err
		}

		for _, name := range names {
			if ignored {
				name = collapse(name)
			}

			seen[name] = stamp(root, name)
		}
	}

	return seen, nil
}

// collapse turns a file inside a folder of a known build cache into the entry of that folder, which ends with a slash.
func collapse(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts[:len(parts)-1] {
		if isBuildCache(part) {
			return strings.Join(parts[:i+1], "/") + "/"
		}
	}

	return name
}

// stamp is the size and the modification time of a file. A file that git lists and that is gone has the stamp `gone`.
// A build cache folder, which ends with a slash, has an empty stamp.
func stamp(root, name string) string {
	if strings.HasSuffix(name, "/") {
		return ""
	}

	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "gone"
	}

	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// besideFolder maps each file below the folder that holds the item folder to its stamp, except the files of the worktrees of the repository, which
// belong to other items. The keys are relative to root and start with `../`.
func besideFolder(ctx context.Context, tree proc.Runner, root string) (map[string]string, error) {
	out, err := git(ctx, tree, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list the worktrees of the repository with git worktree: %w", err)
	}

	owned := map[string]bool{}

	for field := range strings.SplitSeq(out, "\x00") {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			owned[filepath.Clean(path)] = true
		}
	}

	return walkBeside(filepath.Dir(root), owned)
}

// walkBeside maps each file below parent to its stamp, except the files below the folders in owned. An entry that the walk cannot read is an error,
// so an unreadable folder never makes the mark smaller.
func walkBeside(parent string, owned map[string]bool) (map[string]string, error) {
	files := map[string]string{}

	err := filepath.WalkDir(parent, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || owned[path] {
			return besideSkip(walkErr)
		}

		if entry.IsDir() {
			return nil
		}

		stampBeside(files, parent, path)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the folders beside the item folder: %w", err)
	}

	return files, nil
}

// stampBeside puts the stamp of the file at path, below parent, in files under its key that starts with `../`.
func stampBeside(files map[string]string, parent, path string) {
	rel := filepath.ToSlash(strings.TrimLeft(strings.TrimPrefix(path, parent), string(filepath.Separator)))
	files["../"+rel] = stamp(parent, rel)
}

// besideSkip is the result of the walk for an entry that is owned or unreadable: the read error, or a skip of the owned folder.
func besideSkip(walkErr error) error {
	if walkErr != nil {
		return walkErr
	}

	return filepath.SkipDir
}

// listOthers lists the untracked files, or the ignored files.
// The paths end with a NUL byte, so a name with a space or a non-ASCII letter stays whole.
func listOthers(ctx context.Context, tree proc.Runner, ignored bool) ([]string, error) {
	query := []string{"ls-files", "--others", "--exclude-standard", "--full-name", "-z"}
	if ignored {
		query = append(query, "--ignored")
	}

	out, err := git(ctx, tree, append(query, "--", ":/")...)
	if err != nil {
		return nil, fmt.Errorf("list the changed paths of the item folder with git %s: %w", query[0], err)
	}

	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 }), nil
}

// changed lists the paths that the folder changed since the mark: every tracked change, committed or not, and every untracked or ignored
// file that is new, gone or has another size or time, and every file beside the item folder that is new, gone or has another size or time.
// The diff runs with no rename detection, so a rename lists the old path as well as the new one. Every path is relative to the root of the repository.
func changed(ctx context.Context, tree proc.Runner, mark startMark) ([]string, error) {
	now, err := readState(ctx, tree)
	if err != nil {
		return nil, err
	}

	out, err := git(ctx, tree, "diff", "--name-only", "--no-renames", "-z", mark.Head)
	if err != nil {
		return nil, fmt.Errorf("list the changed paths of the item folder with git diff: %w", err)
	}

	names := append(slices.Collect(strings.SplitSeq(out, "\x00")), differ(mark.Seen, now.seen)...)

	if mark.Outside != nil {
		names = append(names, differ(mark.Outside, now.outside)...)
	}

	return names, nil
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
