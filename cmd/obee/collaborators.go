package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
)

var (
	errEmptyCommand = errors.New("the command step has no program to run")
	errNoHost       = errors.New("the step names a host, and the project has no host block")
	errHostJob      = errors.New("the host job did not succeed")
	errNotListed    = errors.New("the folder of the item exists and git does not list it as a worktree")
)

// folderMode is the mode of a folder that holds worktrees or clones.
const (
	folderMode  = 0o700
	gitWorktree = "worktree"
)

// worktrees finds the worktree of an item under root, in the folder <root>/<project>/<item>. The folder must be a worktree that git lists.
// git returns the runner of git for a folder. Ensure adds a missing worktree from the clone of the project in <repos>/<project>.
// forge builds the forge of a project, which names the URL of the clone.
type worktrees struct {
	root     string
	repos    string
	projects map[string]config.Project
	git      func(dir string) proc.Runner
	forge    func(model.Project) (forge.Forge, error)
}

// Ensure returns the worktree of the item. An item with no worktree gets one, cut from the base branch of the project on a new branch named for the item.
// If the branch of the item exists, the worktree uses it as it is, so earlier commits stay. A resume finds the worktree again and changes nothing.
// A folder that git does not list as a worktree is an error. Ensure never deletes it.
func (w worktrees) Ensure(ctx context.Context, project, item string) (string, error) {
	dir, err := w.Path(ctx, project, item)
	if err != nil || dir != "" {
		return dir, err
	}

	found, err := w.project(project)
	if err != nil {
		return "", err
	}

	dir = filepath.Join(w.root, escapeName(project), escapeName(item))
	if err = absent(dir); err != nil {
		return "", err
	}

	return w.create(ctx, found, dir, item)
}

// create refreshes the clone of the project and adds the worktree dir of the item from it.
func (w worktrees) create(ctx context.Context, project config.Project, dir, item string) (string, error) {
	clone := filepath.Join(w.repos, escapeName(project.Name))
	if err := w.refresh(ctx, project, clone); err != nil {
		return "", err
	}

	return w.add(ctx, clone, dir, item, project.BaseBranch)
}

// project returns the config of the named project.
func (w worktrees) project(name string) (config.Project, error) {
	found, ok := w.projects[name]
	if !ok {
		return config.Project{}, fmt.Errorf("%w: %q", errUnknownProject, name)
	}

	return found, nil
}

// absent fails if dir exists. Ensure calls it for a folder that Path did not find, so git does not list the folder.
func absent(dir string) error {
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%w: %s. Move it away, then run the item again", errNotListed, dir)
	}

	return nil
}

// add makes the worktree dir from the clone. It uses the branch named item if the clone has one, and else starts that branch at the base branch. It returns dir.
func (w worktrees) add(ctx context.Context, clone, dir, item, base string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), folderMode); err != nil {
		return "", fmt.Errorf("make the folder for the worktree of item %s: %w", item, err)
	}

	args := []string{gitWorktree, "add", "-b", item, dir, "origin/" + base}
	if w.git(clone).Run(ctx, []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + item}).ExitCode == 0 {
		args = []string{gitWorktree, "add", dir, item}
	}

	if err := w.run(ctx, clone, args...); err != nil {
		return "", err
	}

	return dir, nil
}

// refresh makes the clone of the project if it is missing, and fetches the base branch into it.
func (w worktrees) refresh(ctx context.Context, project config.Project, clone string) error {
	if err := w.clone(ctx, project, clone); err != nil {
		return err
	}

	return w.run(ctx, clone, "fetch", "origin", project.BaseBranch)
}

// clone makes the clone of the repository of the project in dir, unless it is there.
func (w worktrees) clone(ctx context.Context, project config.Project, dir string) error {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil {
		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read the clone of project %s: %w", project.Name, err)
	}

	if err = os.MkdirAll(w.repos, folderMode); err != nil {
		return fmt.Errorf("make the folder for the clone of project %s: %w", project.Name, err)
	}

	remote, err := w.forge(project.Project)
	if err != nil {
		return err
	}

	return w.run(ctx, w.repos, "clone", remote.CloneURL(), dir)
}

// run runs git with args in dir and returns an error that carries what git printed when it fails.
func (w worktrees) run(ctx context.Context, dir string, args ...string) error {
	out := w.git(dir).Run(ctx, args)
	if out.ExitCode != 0 {
		return fmt.Errorf("git %s in %s exited with code %d: %s", args[0], dir, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	return nil
}

// escapeName makes a project or item name safe as one folder name: a separator or a dot never changes the folder.
func escapeName(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), ".", "%2E")
}

// Path returns the worktree of the item, or an empty path when the item has none.
func (w worktrees) Path(ctx context.Context, project, item string) (string, error) {
	dir := filepath.Join(w.root, escapeName(project), escapeName(item))

	want, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("read the folder %s: %w", dir, err)
	}

	return w.ifListed(ctx, dir, want)
}

// ifListed returns dir when git lists the folder want as a worktree, and an empty path when it does not.
func (w worktrees) ifListed(ctx context.Context, dir, want string) (string, error) {
	listed, err := w.listed(ctx, dir)
	if err != nil {
		return "", err
	}

	if slices.ContainsFunc(listed, func(path string) bool {
		real, err := filepath.EvalSymlinks(path)

		return err == nil && real == want
	}) {
		return dir, nil
	}

	return "", nil
}

// listed returns the path of each worktree that git lists for the repository of dir.
func (w worktrees) listed(ctx context.Context, dir string) ([]string, error) {
	out := w.git(dir).Run(ctx, []string{gitWorktree, "list", "--porcelain"})
	if out.ExitCode != 0 {
		return nil, fmt.Errorf("git worktree list in %s exited with code %d: %s", dir, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	var paths []string

	for _, line := range strings.Split(out.Stdout, "\n") {
		if path, found := strings.CutPrefix(line, "worktree "); found {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// treeFinder is what the command runner needs of the worktrees.
type treeFinder interface {
	Path(ctx context.Context, project, item string) (string, error)
}

// programs runs the programs of command steps. A step with no host runs in the worktree of the item, or in the folder of the process when the item has none.
// A step with a host runs as one job on the host of the project. A host job has no output to read: its stdout is empty and its code is 0 or 1.
type programs struct {
	trees treeFinder
	local func(binary, dir string, timeout time.Duration) proc.Runner
	hosts func(project string) (host.Host, error)
}

// Run runs the program of the request and returns what it printed and its exit code.
func (p programs) Run(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	if len(request.Run) == 0 {
		return flow.CommandOutput{}, errEmptyCommand
	}

	if request.Host != "" {
		return p.onHost(ctx, request)
	}

	dir, err := p.trees.Path(ctx, request.Project, request.Item)
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("find the folder of item %s: %w", request.Item, err)
	}

	result := p.local(request.Run[0], dir, request.Timeout).Run(ctx, request.Run[1:])

	return flow.CommandOutput{Stdout: result.Stdout, RC: result.ExitCode}, nil
}

func (p programs) onHost(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	h, err := p.hosts(request.Project)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	id, err := h.Submit(ctx, model.JobSpec{Name: "obee-" + request.Item, Command: shellLine(request.Run)})
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("submit the command of item %s to the host: %w", request.Item, err)
	}

	state, err := host.WaitJob(ctx, h, id)
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("wait for the command of item %s on the host: %w", request.Item, err)
	}

	return hostOutput(state)
}

// hostOutput maps the end state of a host job to an exit code. Any state other than succeeded or failed is an error.
func hostOutput(state model.JobState) (flow.CommandOutput, error) {
	if state == model.JobSucceeded {
		return flow.CommandOutput{}, nil
	}

	if state == model.JobFailed {
		return flow.CommandOutput{RC: 1}, nil
	}

	return flow.CommandOutput{}, fmt.Errorf("%w: the state is %s", errHostJob, state)
}

// shellLine quotes each word and joins them with spaces.
func shellLine(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = shellQuote(word)
	}

	return strings.Join(quoted, " ")
}

// shellQuote quotes one word for a POSIX shell.
func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// trackerItems reads the fields of an item from the tracker of its project.
type trackerItems struct {
	projects map[string]config.Project
	tracker  func(model.Project) (store.Tracker, error)
}

// Fields returns the fields that the tracker declares for the item.
func (t trackerItems) Fields(ctx context.Context, project, item string) (map[string]string, error) {
	found, ok := t.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := t.tracker(found.Project)
	if err != nil {
		return nil, err
	}

	return tracker.Item(ctx, item)
}
