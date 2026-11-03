package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/lease"
	"github.com/tobocop2/obee/internal/lockfile"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/review"
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
	// lockRetry is how often a start that waits for the lock of a clone tries it again.
	lockRetry = 25 * time.Millisecond
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
	if w.plain(project) {
		return w.ensurePlain(project, item)
	}

	dir, err := w.Path(ctx, project, item)
	if err != nil || dir != "" {
		return dir, err
	}

	found, err := w.project(project)
	if err != nil {
		return "", err
	}

	dir = filepath.Join(w.root, model.SafeName(project), model.SafeName(item))
	if err = absent(dir); err != nil {
		return "", err
	}

	return w.create(ctx, found, dir, item)
}

// plain reports whether the named project has no repository. Its item folder is then a plain folder that obee makes, and git never reads it.
// A project that the finder does not know is not plain.
func (w worktrees) plain(project string) bool {
	found, ok := w.projects[project]

	return ok && !found.HasRepository()
}

// ensurePlain returns the plain folder of the item and makes it when it is missing. A resume finds the folder again with its files.
func (w worktrees) ensurePlain(project, item string) (string, error) {
	dir := filepath.Join(w.root, model.SafeName(project), model.SafeName(item))
	if err := os.MkdirAll(dir, folderMode); err != nil {
		return "", fmt.Errorf("make the folder of item %s: %w", item, err)
	}

	return dir, nil
}

// create refreshes the clone of the project and adds the worktree dir of the item from it.
// It holds a file lock of the project for the whole call. The clone and every worktree add of one project run one at a time,
// so the first start makes the clone and each later start finds it ready. A file lock also holds across engines that share the repos folder.
func (w worktrees) create(ctx context.Context, project config.Project, dir, item string) (added string, err error) {
	if err = os.MkdirAll(w.repos, folderMode); err != nil {
		return "", fmt.Errorf("make the folder for the clone of project %s: %w", project.Name, err)
	}

	clone := filepath.Join(w.repos, model.SafeName(project.Name))

	held := lockfile.Open(clone + ".lock")
	if _, err = held.TryLockContext(ctx, lockRetry); err != nil {
		return "", fmt.Errorf("lock the clone of project %s: %w", project.Name, err)
	}

	defer func() { err = errors.Join(err, held.Unlock()) }()

	// The lock also covers the fetch, because two fetches into one clone race on its ref locks.

	if err = w.refresh(ctx, project, clone); err != nil {
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

// clone makes the clone of the repository of the project in dir, unless it is there. The clone comes from the source of the project, or else from its forge.
func (w worktrees) clone(ctx context.Context, project config.Project, dir string) error {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil {
		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read the clone of project %s: %w", project.Name, err)
	}

	remote, err := cloneSource(project.Project, w.forge)
	if err != nil {
		return err
	}

	return w.run(ctx, w.repos, "clone", remote, dir)
}

// cloneSource returns what git clones the repository of the project from: its source when the project sets one, else the clone URL of its forge.
// A project with neither gets the error of the forge, which matches forge.ErrNoForge.
func cloneSource(project model.Project, forges func(model.Project) (forge.Forge, error)) (string, error) {
	if project.Source != "" {
		return project.Source, nil
	}

	remote, err := forges(project)
	if err != nil {
		return "", err
	}

	return remote.CloneURL(), nil
}

// run runs git with args in dir and returns an error that carries what git printed when it fails.
func (w worktrees) run(ctx context.Context, dir string, args ...string) error {
	out := w.git(dir).Run(ctx, args)
	if out.ExitCode != 0 {
		return fmt.Errorf("git %s in %s exited with code %d: %s", args[0], dir, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	return nil
}

// Path returns the worktree of the item, or an empty path when the item has none.
func (w worktrees) Path(ctx context.Context, project, item string) (string, error) {
	dir := filepath.Join(w.root, model.SafeName(project), model.SafeName(item))

	want, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("read the folder %s: %w", dir, err)
	}

	if w.plain(project) {
		return dir, nil
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

// programs runs the programs of command steps. A step with no host runs in the worktree of the item, or in a new empty folder that obee removes afterwards when the item has none.
// A step with a host runs as one job on the host of the project. A host job has no output to read: its stdout is empty and its code is 0 or 1.
type programs struct {
	trees treeFinder
	// local runs a program with the environment of the process. The gate uses it.
	local func(binary, dir string, timeout time.Duration) proc.Runner
	// exec runs the program of a command step. Its spec holds the whole environment of the program (Only) and its stdin.
	exec  func(spec proc.ExecRunner) proc.Runner
	hosts func(project string) (host.Host, error)
	// sandbox runs a gate under the sandbox of its project.
	sandbox gateSandbox
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

	if request.Gate && p.sandbox.applies(request.Project, true) {
		return p.sandboxed(ctx, request, dir)
	}

	return p.inFolder(ctx, request, dir)
}

// sandboxed runs the program of a gate under the sandbox of its project. Stdin and the process cap of the request do not apply to a sandboxed gate.
// The output is the plan, then what the program printed, with stdout and stderr in one stream.
func (p programs) sandboxed(ctx context.Context, request flow.CommandRequest, dir string) (flow.CommandOutput, error) {
	own := dir == ""

	dir, cleanup, err := commandFolder(dir, request.Item)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	defer cleanup()

	run := p.sandbox.run
	if own {
		run = p.sandbox.runOwn
	}

	out, code := run(ctx, request.Project, dir, request.Run, request.Env, request.Timeout)

	return flow.CommandOutput{Stdout: out, RC: code}, nil
}

// inFolder runs the program of the request in dir, or in a new empty folder when dir is empty.
func (p programs) inFolder(ctx context.Context, request flow.CommandRequest, dir string) (flow.CommandOutput, error) {
	dir, cleanup, err := commandFolder(dir, request.Item)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	defer cleanup()

	spec := proc.ExecRunner{Binary: request.Run[0], Dir: dir, Timeout: request.Timeout, Only: request.Env, Stdin: request.Stdin, MaxNewProcesses: request.MaxNewProcesses}
	result := p.exec(spec).Run(ctx, request.Run[1:])

	if result.SetupErr != nil {
		return flow.CommandOutput{}, result.SetupErr
	}

	return flow.CommandOutput{Stdout: result.Stdout + capNote(request.MaxNewProcesses, result), Stderr: result.Stderr, RC: result.ExitCode}, nil
}

// showsForkFailure reports whether text holds a sign of a failed process start. The texts are the system error for EAGAIN ("Resource temporarily unavailable"), the message of dash ("Cannot fork") and the message of bash ("fork: retry:").
// A program can print the same words by itself, so the note says only that a start failed under a cap, and it is added only to a failed command.
func showsForkFailure(text string) bool {
	return strings.Contains(text, "Resource temporarily unavailable") || strings.Contains(text, "Cannot fork") || strings.Contains(text, "fork: retry:")
}

// capNote is the line that ends the output of a failed command when the output shows a failed process start under a process cap, so the record names the cap.
// It is empty for a command with no cap, with exit code 0, or whose failure shows no fork failure text in its output.
func capNote(limit int, result model.CommandResult) string {
	if limit == 0 || result.ExitCode == 0 {
		return ""
	}

	if !showsForkFailure(result.Stdout + "\n" + result.Stderr) {
		return ""
	}

	return fmt.Sprintf("\nobee: a process start failed. The command ran with max_new_processes = %d: the process count of the user at the start plus %d, shared with the other programs of the user.\n", limit, limit)
}

// commandFolder returns dir, or a new empty folder when dir is empty because the item has no worktree. The program then never touches the folder where obee started.
// The returned function removes a new folder, and does nothing for dir.
func commandFolder(dir, item string) (string, func(), error) {
	if dir != "" {
		return dir, func() {}, nil
	}

	scratch, err := os.MkdirTemp("", "obee-command-")
	if err != nil {
		return "", nil, fmt.Errorf("make an empty folder for the command of item %s: %w", item, err)
	}

	return scratch, func() { _ = os.RemoveAll(scratch) }, nil
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

	return flow.CommandOutput{}, fmt.Errorf("%w: %s", errHostJob, hostAdvice(state))
}

// hostAdvice says what a person can do about a host job that ended in a state other than succeeded or failed.
func hostAdvice(state model.JobState) string {
	switch state {
	case model.JobDied:
		return "the job died: the scheduler holds no record of it. Run the command again"
	case model.JobStale:
		return "the job was stopped because its log did not grow for `host.stale_minutes`. Read the log on the host, raise `host.stale_minutes` if the command is slow, then run it again"
	case model.JobTimedOut:
		return "the job was stopped by the run limit `host.max_run`. Raise `host.max_run` if the command is slow, then run it again"
	case model.JobPending, model.JobRunning, model.JobCanceled, model.JobUnknown, model.JobLost, model.JobSucceeded, model.JobFailed:
	}

	return "the state is " + string(state)
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
	tracker  trackerBuilder
}

// Fields returns the fields that the tracker declares for the item.
func (t trackerItems) Fields(ctx context.Context, project, item string) (map[string]string, error) {
	found, ok := t.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := t.tracker(ctx, found.Project)
	if err != nil {
		return nil, err
	}

	fields, err := tracker.Item(ctx, item)

	return fields, closeAfter(err, tracker)
}

// closeAfter closes what holds a file, and returns err joined with the error of the close. What holds no file is left as it is.
func closeAfter(err error, opened any) error {
	closer, ok := opened.(io.Closer)
	if !ok {
		return err
	}

	if closeErr := closer.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}

	return err
}

var errManyPull = errors.New("the item has more than one pull request")

// pullListLimit is the limit of one pull request search. A list at the limit is refused, so it must be larger than one.
const pullListLimit = 5

// namedListLimit is the limit of the search for pull requests that name an item. A list at the limit is refused, so it must be larger than the open pull requests that can match.
const namedListLimit = 50

// pulls reads and changes the pull request of an item. The pull request is the one on the branch that is named for the item.
type pulls struct {
	projects map[string]config.Project
	forge    func(model.Project) (forge.Forge, error)
	runners  []string
}

// opened is the forge of one project. It passes the optional reads of the merge (trees and failed logs) to the forge when the forge has them:
// an embedded interface hides every method that it does not list, so the merge would not see them without these methods.
type opened struct{ forge.Forge }

var errNoOptionalRead = errors.New("this forge cannot read it")

// CommitTree returns the tree id of a commit, when the forge reads trees.
func (o opened) CommitTree(ctx context.Context, commit string) (string, error) {
	trees, ok := o.Forge.(merge.Trees)
	if !ok {
		return "", fmt.Errorf("%w: trees", errNoOptionalRead)
	}

	return trees.CommitTree(ctx, commit)
}

// TreeBlobs returns the files of a tree, when the forge reads trees.
func (o opened) TreeBlobs(ctx context.Context, tree string) (map[string]string, error) {
	trees, ok := o.Forge.(merge.Trees)
	if !ok {
		return nil, fmt.Errorf("%w: trees", errNoOptionalRead)
	}

	return trees.TreeBlobs(ctx, tree)
}

// IsAncestor tells whether a commit is in the history of the base, when the forge compares commits.
func (o opened) IsAncestor(ctx context.Context, commit, base string) (bool, error) {
	history, ok := o.Forge.(merge.Ancestry)
	if !ok {
		return false, fmt.Errorf("%w: ancestry", errNoOptionalRead)
	}

	return history.IsAncestor(ctx, commit, base)
}

// FailedLog returns the log of a failed check, when the forge reads logs.
func (o opened) FailedLog(ctx context.Context, link string) (string, error) {
	logs, ok := o.Forge.(merge.Logs)
	if !ok {
		return "", fmt.Errorf("%w: logs", errNoOptionalRead)
	}

	return logs.FailedLog(ctx, link)
}

// forgeOf builds the forge of the named project.
func (p pulls) forgeOf(name string) (opened, error) {
	found, err := p.projectOf(name)
	if err != nil {
		return opened{}, err
	}

	built, err := p.forge(found.Project)

	return opened{built}, err
}

// projectOf returns the named project. A name that the config lacks is an error, so no request is built for a project that obee does not know.
func (p pulls) projectOf(name string) (config.Project, error) {
	found, ok := p.projects[name]
	if !ok {
		return config.Project{}, fmt.Errorf("%w: %q", errUnknownProject, name)
	}

	return found, nil
}

// find returns the forge of the project and the number of the pull request of the item.
func (p pulls) find(ctx context.Context, project, item string) (opened, int, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return opened{}, 0, err
	}

	return p.numberOf(ctx, built, item)
}

// numberOf returns the forge and the number of the pull request of the item on that forge.
func (p pulls) numberOf(ctx context.Context, built opened, item string) (opened, int, error) {
	// The open pull request comes first. A merged one is found so that a resumed watch reads it.
	for _, state := range []string{"open", "merged"} {
		number, err := listed(ctx, built, item, state)
		if err != nil || number != 0 {
			return built, number, err
		}
	}

	return opened{}, 0, fmt.Errorf("%w: no pull request is on the branch %s", forge.ErrNoPull, item)
}

// listed returns the number of the one pull request in the state on the branch of the item, or 0 when there is none.
func listed(ctx context.Context, built forge.Forge, item, state string) (int, error) {
	rows, err := built.PRList(ctx, pullListLimit, "--head", item, "--state", state, "--json", "number")
	if err != nil {
		return 0, fmt.Errorf("find the pull request of item %s: %w", item, err)
	}

	if len(rows) > 1 {
		return 0, fmt.Errorf("%w: item %s has %d %s pull requests", errManyPull, item, len(rows), state)
	}

	if len(rows) == 0 {
		return 0, nil
	}

	return rows[0].Number, nil
}

// Pull reads what the forge shows about the pull request of the item. A read that fails is a reason in the standing.
func (p pulls) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	built, number, err := p.find(ctx, project, item)
	if err != nil {
		return review.Standing{}, err
	}

	return review.Pull(ctx, built, number), nil
}

// Reviewed reads the standing as Pull does, then lets a review of the commit reviewed stand for a head that holds the same tree.
func (p pulls) Reviewed(ctx context.Context, project, item, reviewed string) (review.Standing, error) {
	built, number, err := p.find(ctx, project, item)
	if err != nil {
		return review.Standing{}, err
	}

	standing := review.Pull(ctx, built, number)
	standing.Compare(ctx, built, reviewed)

	return standing, nil
}

// Ready takes the pull request of the item out of draft.
func (p pulls) Ready(ctx context.Context, project, item string) error {
	built, number, err := p.find(ctx, project, item)
	if err != nil {
		return err
	}

	return built.SetDraft(ctx, number, false)
}

// ClosingIssues reads the issues that merging the pull request of the item closes, as the forge lists them.
// An item with no pull request, and a project with no forge, give found false: there is no scope to fix.
// GitHub lists closing references only for a pull request into the default branch, so a pull request into another base has an empty set.
func (p pulls) ClosingIssues(ctx context.Context, project, item string) (issues []int, found bool, err error) {
	built, number, err := p.find(ctx, project, item)
	if errors.Is(err, forge.ErrNoPull) || errors.Is(err, forge.ErrNoForge) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	refs, err := built.ClosingRefs(ctx, number)
	if err != nil {
		return nil, false, fmt.Errorf("read the issues that pull request %d closes: %w", number, err)
	}

	return refs.Issues, true, nil
}

// Watch reads the pull request once.
func (p pulls) Watch(ctx context.Context, project string, pr int) (review.Outcome, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return review.Outcome{}, err
	}

	return review.Watch(ctx, built, pr)
}

// Merge merges the pull request of the item at head, through the guards of the merge package and the settings of the merge step.
// The runners of the config count as own identities, next to the logins and addresses of the step. A pattern that is not RE2 is an error.
func (p pulls) Merge(ctx context.Context, project, item, head string, settings flow.MergeSettings) (merge.Result, error) {
	found, rules, err := p.prepared(project, settings.ForbidText)
	if err != nil {
		return merge.Result{}, err
	}

	built, number, err := p.target(ctx, project, item, settings.Repo)

	if result, wrong := repoRefusal(err); wrong {
		return result, nil
	}

	if err != nil {
		return merge.Result{}, err
	}

	return merge.Run(ctx, built, p.request(project, found, item, number, head, settings, rules)), nil
}

// Preflight judges the pull request of the item. A closed pull request counts, so a close is a stop and not a missing pull request.
// An item with no pull request in any state has nothing that can have changed, so it gives Unchanged.
func (p pulls) Preflight(ctx context.Context, project, item string, req preflight.Request, hold string) (preflight.Result, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return preflight.Result{}, err
	}

	number, err := anyState(ctx, built, item)
	if err != nil {
		return preflight.Result{}, err
	}

	if number == 0 {
		return preflight.Result{Stop: preflight.Unchanged, Lines: []string{"OK: the item has no pull request yet."}}, nil
	}

	req.PR = number

	return preflight.Check(ctx, built, req, hold)
}

// anyState returns the number of the pull request of the item: the open one on its branch, then an open one that names it, then the merged one, then the newest closed one. It is 0 when there is none.
func anyState(ctx context.Context, built forge.Forge, item string) (int, error) {
	number, err := listed(ctx, built, item, "open")
	if err != nil || number != 0 {
		return number, err
	}

	// An open pull request that names the item outranks a merged or closed one on its branch: the item is not free.
	number, err = namedBy(ctx, built, item)
	if err != nil || number != 0 {
		return number, err
	}

	number, err = listed(ctx, built, item, "merged")
	if err != nil || number != 0 {
		return number, err
	}

	return newestListed(ctx, built, item, "closed")
}

// mentionRun is one run of the characters an item id or a #N reference is made of. The text of a pull request is cut into such runs.
var mentionRun = regexp.MustCompile(`[\w#.-]+`)

// namedBy returns the number of an open pull request that names the item in its title or body, or 0 when none does.
// It finds work on a branch that is not the item: a person or another run opened it under its own branch name.
// Any mention counts, as in "Closes #7", "see #7" or "follow-up to #7": a passing mention makes the item not free. This is the safe direction.
// The search is loose, so the text of each pull request is matched again on the whole item id.
func namedBy(ctx context.Context, built forge.Forge, item string) (int, error) {
	term := mention(item)

	rows, err := built.PRList(ctx, namedListLimit, "--state", "open", "--search", term+" in:title,body", "--json", "number,title,body")
	if err != nil {
		return 0, fmt.Errorf("search the pull requests that name item %s: %w", item, err)
	}

	for _, row := range rows {
		if names(row.Title, term) || names(row.Body, term) {
			return row.Number, nil
		}
	}

	return 0, nil
}

// names reports whether the text holds the term as a whole run. One full stop that ends a sentence after the term does not count as part of it.
func names(text, term string) bool {
	for _, run := range mentionRun.FindAllString(text, -1) {
		if run == term || run == term+"." {
			return true
		}
	}

	return false
}

// mention is the text a pull request holds when it names the item: #N for a number of the forge, the id for any other.
func mention(item string) string {
	if model.ItemKind(item) == model.KindForge {
		return "#" + item
	}

	return item
}

// newestListed returns the number of the newest pull request in the state on the branch of the item, or 0 when there is none.
// A branch that was closed and opened again has many closed pull requests, and only the last one tells where the item stands. The forge lists the newest first.
func newestListed(ctx context.Context, built forge.Forge, item, state string) (int, error) {
	rows, err := built.PRList(ctx, pullListLimit, "--head", item, "--state", state, "--json", "number")
	if err != nil {
		return 0, fmt.Errorf("find the pull request of item %s: %w", item, err)
	}

	if len(rows) == 0 {
		return 0, nil
	}

	return rows[0].Number, nil
}

// request is the merge request of one pull request: the settings of the step, and the runners of the config as own identities.
// The id of an item of the GitHub tracker is the number of its issue, so that issue is the one the pull request must close, and the closing
// references of the pull request are audited against it. An id of any other tracker is a free name, not an issue number, so the audit is off for it,
// and the request carries the line that says so. The caller passes the project it has already found, so a project that obee does not know never gets here.
func (p pulls) request(name string, project config.Project, item string, number int, head string, settings flow.MergeSettings, rules []*regexp.Regexp) merge.Request {
	names, unaudited := audited(name, project.Tracker, item)

	return merge.Request{
		AuditClosing: names != nil, Names: names, Kept: settings.Kept, Unaudited: unaudited,
		PR: number, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: p.runners,
		OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved,
		Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget, GatedTree: settings.GatedTree, Intent: settings.Intent,
	}
}

// prepared returns the known project and the compiled forbid_text patterns of a merge step.
func (p pulls) prepared(project string, patterns []string) (config.Project, []*regexp.Regexp, error) {
	rules, err := compileForbidden(patterns)
	if err != nil {
		return config.Project{}, nil, err
	}

	found, err := p.projectOf(project)

	return found, rules, err
}

// repoRefusal turns a repoError into the refusal to show. The second value is false for any other error.
func repoRefusal(err error) (merge.Result, bool) {
	var wrong *repoError
	if !errors.As(err, &wrong) {
		return merge.Result{}, false
	}

	return merge.Result{Stop: merge.Refused, Lines: []string{wrong.Error()}}, true
}

// audited returns the issues that the item names, or the line that says why the audit is off.
func audited(project string, tracker model.TrackerName, item string) ([]int, string) {
	named, err := strconv.Atoi(item)

	switch {
	case tracker != model.TrackerGithub:
		return nil, fmt.Sprintf("closing references: the tracker %s of project %s has no issue numbers, so no audit ran.", tracker, project)
	case err != nil || named <= 0 || strconv.Itoa(named) != item:
		return nil, fmt.Sprintf("closing references: the item id %q of project %s is not a plain issue number, so no audit ran.", item, project)
	}

	return []int{named}, ""
}

// repoError says that a merge step names a repository other than the one of the project. Its text is the line to show.
type repoError struct{ named, project, own string }

func (e *repoError) Error() string {
	return fmt.Sprintf("REFUSE: the merge step names the repository %s, and the project %s uses %s. Set `repo` to %s.", e.named, e.project, e.own, e.own)
}

// target returns the forge of the project and the number of the pull request of the item.
// A repo that is not the repository of the project is a repoError, before any read of the forge. Case does not count.
func (p pulls) target(ctx context.Context, project, item, repo string) (opened, int, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return opened{}, 0, err
	}

	if own := p.projects[project].Repo; !strings.EqualFold(repo, own) {
		return opened{}, 0, &repoError{named: repo, project: project, own: own}
	}

	return p.numberOf(ctx, built, item)
}

// compileForbidden compiles the forbid_text patterns of a merge step.
func compileForbidden(patterns []string) ([]*regexp.Regexp, error) {
	rules := make([]*regexp.Regexp, len(patterns))

	for i, pattern := range patterns {
		rule, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("the forbid_text pattern %q is not valid: %w", pattern, err)
		}

		rules[i] = rule
	}

	return rules, nil
}

// SetDraft sets or clears the draft state of the pull request.
func (p pulls) SetDraft(ctx context.Context, project string, pr int, draft bool) error {
	built, err := p.forgeOf(project)
	if err != nil {
		return err
	}

	return built.SetDraft(ctx, pr, draft)
}

// leases takes, renews and gives back the claim of an item in the claim store of its project.
type leases struct {
	projects map[string]config.Project
	rows     func(context.Context, model.Project) (claimStore, error)
	owner    func() string
}

// with opens the claim store of the named project, runs fn on the claims over it and closes the store.
// A store that fails to close is an error, joined with the error of fn.
func (l leases) with(ctx context.Context, name string, fn func(*claim.Claims) error) error {
	found, ok := l.projects[name]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownProject, name)
	}

	rows, err := l.rows(ctx, found.Project)
	if err != nil {
		return err
	}

	claims, err := lease.Open(rows)
	if err == nil {
		err = fn(claims)
	}

	return errors.Join(err, rows.Close())
}

// Claim takes the item for a new owner and returns the token of that owner.
func (l leases) Claim(ctx context.Context, project, item string) (string, error) {
	owner := l.owner()

	if err := l.with(ctx, project, func(claims *claim.Claims) error { return claims.Take(ctx, owner, item) }); err != nil {
		return "", err
	}

	return owner, nil
}

// Others lists the other orchestrators that hold items in the project, one line for each: the owner and its items. A store that cannot be read is an error.
func (l leases) Others(ctx context.Context, project, owner string) ([]string, error) {
	var lines []string

	err := l.with(ctx, project, func(claims *claim.Claims) error {
		others, err := claims.Others(ctx, owner)

		for _, other := range others {
			lines = append(lines, other.Holder+": "+strings.Join(other.Items, ", "))
		}

		return err
	})

	return lines, err
}

// Release gives the item back for the owner.
func (l leases) Release(ctx context.Context, project, item, owner string) error {
	return l.with(ctx, project, func(claims *claim.Claims) error { return claims.Release(ctx, owner, item) })
}

// Beat renews the claims of the owner. A text means the claim store says the claim is gone.
func (l leases) Beat(ctx context.Context, project, owner string) (string, error) {
	var text string

	err := l.with(ctx, project, func(claims *claim.Claims) error {
		var err error

		text, err = claims.Beat(ctx, owner)

		return err
	})

	return text, err
}

// Renewed reads when the lease of the claim of the owner on the item was last renewed. The zero time means the owner holds no claim on the item.
func (l leases) Renewed(ctx context.Context, project, owner, item string) (time.Time, error) {
	var renewed time.Time

	err := l.with(ctx, project, func(claims *claim.Claims) error {
		var err error

		renewed, err = claims.Renewed(ctx, owner, item)

		return err
	})

	return renewed, err
}

// ownerToken returns a new owner token: letters and digits, and no two calls alike.
func ownerToken() string { return "obee" + rand.Text() }

// itemLife is the lifecycle of an item: the claim from the claim store and the pull request from the forge.
type itemLife struct {
	leases
	pulls
}
