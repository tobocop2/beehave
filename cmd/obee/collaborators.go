package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/lease"
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

// clone makes the clone of the repository of the project in dir, unless it is there. The clone comes from the source of the project, or else from its forge.
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

// programs runs the programs of command steps. A step with no host runs in the worktree of the item, or in a new empty folder that obee removes afterwards when the item has none.
// A step with a host runs as one job on the host of the project. A host job has no output to read: its stdout is empty and its code is 0 or 1.
type programs struct {
	trees treeFinder
	// local runs a program with the environment of the process. The gate uses it.
	local func(binary, dir string, timeout time.Duration) proc.Runner
	// exec runs the program of a command step. Its spec holds the whole environment of the program (Only) and its stdin.
	exec  func(spec proc.ExecRunner) proc.Runner
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

	return p.inFolder(ctx, request, dir)
}

// inFolder runs the program of the request in dir, or in a new empty folder when dir is empty.
func (p programs) inFolder(ctx context.Context, request flow.CommandRequest, dir string) (flow.CommandOutput, error) {
	dir, cleanup, err := commandFolder(dir, request.Item)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	defer cleanup()

	spec := proc.ExecRunner{Binary: request.Run[0], Dir: dir, Timeout: request.Timeout, Only: request.Env, Stdin: request.Stdin}
	result := p.exec(spec).Run(ctx, request.Run[1:])

	return flow.CommandOutput{Stdout: result.Stdout, RC: result.ExitCode}, nil
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

var (
	errNoPull   = errors.New("the item has no pull request")
	errManyPull = errors.New("the item has more than one pull request")
)

// pullListLimit is the limit of one pull request search. A list at the limit is refused, so it must be larger than one.
const pullListLimit = 5

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
	found, ok := p.projects[name]
	if !ok {
		return opened{}, fmt.Errorf("%w: %q", errUnknownProject, name)
	}

	built, err := p.forge(found.Project)

	return opened{built}, err
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

	return opened{}, 0, fmt.Errorf("%w: no pull request is on the branch %s", errNoPull, item)
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
	rules, err := compileForbidden(settings.ForbidText)
	if err != nil {
		return merge.Result{}, err
	}

	built, number, err := p.target(ctx, project, item, settings.Repo)

	var wrong *repoError
	if errors.As(err, &wrong) {
		return merge.Result{Stop: merge.Refused, Lines: []string{wrong.Error()}}, nil
	}

	if err != nil {
		return merge.Result{}, err
	}

	return merge.Run(ctx, built, p.request(number, head, settings, rules)), nil
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

// anyState returns the number of the pull request of the item: the open one first, then the merged one, then the newest closed one. It is 0 when there is none.
func anyState(ctx context.Context, built forge.Forge, item string) (int, error) {
	for _, state := range []string{"open", "merged"} {
		number, err := listed(ctx, built, item, state)
		if err != nil || number != 0 {
			return number, err
		}
	}

	return newestListed(ctx, built, item, "closed")
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
func (p pulls) request(number int, head string, settings flow.MergeSettings, rules []*regexp.Regexp) merge.Request {
	return merge.Request{
		PR: number, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: p.runners,
		OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved,
		Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget, GatedTree: settings.GatedTree, Intent: settings.Intent,
	}
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

// ownerToken returns a new owner token: letters and digits, and no two calls alike.
func ownerToken() string { return "obee" + rand.Text() }

// itemLife is the lifecycle of an item: the claim from the claim store and the pull request from the forge.
type itemLife struct {
	leases
	pulls
}
