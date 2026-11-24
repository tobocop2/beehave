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

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/lease"
	"github.com/tobocop2/beehave/internal/lockfile"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/review"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

var (
	errEmptyCommand = errors.New("the command step has no program to run")
	errNoHost       = errors.New("the step names a host, and the project has no host block")
	errHostJob      = errors.New("the host job did not succeed")
	errNotListed    = errors.New("the folder of the item exists and git does not list it as a worktree")
	errKeptChanges  = errors.New("a fresh run of a round item does not reuse a worktree with changes")
)

// folderMode is the mode of a folder that holds worktrees or clones.
const (
	folderMode  = 0o700
	gitWorktree = "worktree"
	gitAdd      = "add"
	// placedMarks is the folder of the per-worktree ref that marks the commit that beehave placed a round worktree at. Git deletes it with the worktree.
	placedMarks = "refs/worktree/beehave/placed"
	placedMark  = placedMarks + "/tree"
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
	// git runs git in the clone and its worktrees, with the file transport only. remote replaces the runner of the owner for the calls that reach the
	// remote of a project with no credentials; nil runs the real one (see reach).
	git    func(dir string) proc.Runner
	remote func(dir string) proc.Runner
	// pushCheck checks the config of the push repository before each call that reaches a remote; nil means the allow list.
	pushCheck func(ctx context.Context, dir string) error
	forge     func(model.Project) (forge.Forge, error)
	// roles gives a call that reaches the remote of a project the writer role of that project. A project with no credentials reaches it as the owner. A
	// project with credentials and no loaded role is refused. newWorktrees is the only constructor, so every value holds the roles of its caller.
	roles *writers
}

// Ensure returns the worktree of the item. An item with no worktree gets one, cut from base on a new branch named for the item.
// An empty base means the base branch of the project. A child item of a round passes the base of its round.
// If the branch of the item exists, the worktree uses it as it is, so earlier commits stay. A resume finds the worktree again and changes nothing.
// A round item gets a detached worktree at start, or at the tip of its own base branch on the remote when start is empty.
// With an empty start, a round worktree that exists moves to that tip when it is clean and is refused when it holds changes. Any other item ignores start.
// A round item ignores base. A folder that git does not list as a worktree is an error. Ensure never deletes it. The lookup and the add run under the lock of the clone.
func (w worktrees) Ensure(ctx context.Context, project, item, start, base string) (string, error) {
	if w.plain(project) {
		return w.ensurePlain(project, item)
	}

	found, err := w.project(project)
	if err != nil {
		return "", err
	}

	var dir string

	if err := (cloneLock{repos: w.repos}).hold(ctx, project, func(clone string) (held error) {
		dir, held = w.ensureLocked(ctx, found, clone, item, start, base)

		return held
	}); err != nil {
		return "", err
	}

	return dir, nil
}

// ensureLocked finds or adds the worktree of the item while Ensure holds the lock of the clone.
func (w worktrees) ensureLocked(ctx context.Context, project config.Project, clone, item, start, base string) (string, error) {
	dir, err := w.lookup(ctx, project.Name, item)
	if err != nil {
		return "", err
	}

	if dir == "" {
		return w.addLocked(ctx, project, clone, item, start, base)
	}

	if base, round := model.RoundBase(item); round && start == "" {
		if err := w.moveToTip(ctx, project, clone, dir, base, item); err != nil {
			return "", err
		}
	}

	return dir, nil
}

// moveToTip moves the kept worktree of a round item to the tip of its base on the remote. A worktree with tracked or untracked changes, or with commits
// that no remote branch holds, is refused, and the error names it, the commits and how to remove it.
func (w worktrees) moveToTip(ctx context.Context, project config.Project, clone, dir, base, item string) error {
	if err := w.refuseChanges(ctx, clone, dir, item, "an earlier run left"); err != nil {
		return err
	}

	if err := w.fetchBase(ctx, project, clone, base, item); err != nil {
		return err
	}

	if err := w.refuseUnpushed(ctx, clone, dir, item); err != nil {
		return err
	}

	return w.moveTo(ctx, dir, "origin/"+base)
}

// Place puts the worktree of the item at the commit head, for a fork. A round item gets a detached worktree at head. Any other item gets branch, cut at head,
// and the branch of the item stays as it is. A commit that the clone lacks is refused and named. So is a worktree with changes, and a round worktree that
// holds commits that no remote branch holds and that head does not hold. A project with no repository has nothing to place.
func (w worktrees) Place(ctx context.Context, project, item, head, branch string) error {
	if w.plain(project) {
		return nil
	}

	found, err := w.project(project)
	if err != nil {
		return err
	}

	return cloneLock{repos: w.repos}.hold(ctx, project, func(clone string) error { return w.placeLocked(ctx, found, clone, item, head, branch) })
}

// placeLocked is Place while Place holds the lock of the clone.
func (w worktrees) placeLocked(ctx context.Context, project config.Project, clone, item, head, branch string) error {
	if err := w.run(ctx, clone, "cat-file", "-e", head+"^{commit}"); err != nil {
		return fmt.Errorf("the clone of project %s does not hold the commit %s that the fork starts at. Start the item again: beehave run %s: %w", project.Name, head, item, err)
	}

	dir, err := w.lookup(ctx, project.Name, item)
	if err != nil {
		return err
	}

	if dir == "" {
		return w.placeNew(ctx, clone, filepath.Join(w.root, model.SafeName(project.Name), model.SafeName(item)), item, head, branch)
	}

	return w.placeKept(ctx, clone, dir, item, head, branch)
}

// placeNew adds the worktree of the item at head. A tracker item gets branch, which a try of the same attempt before it can have made. Git drops the record of a removed folder first, so that record does not hold the branch.
func (w worktrees) placeNew(ctx context.Context, clone, dir, item, head, branch string) error {
	if _, round := model.RoundBase(item); round {
		_, err := w.add(ctx, clone, dir, item, head)

		return err
	}

	if err := os.MkdirAll(filepath.Dir(dir), folderMode); err != nil {
		return fmt.Errorf("make the folder for the worktree of item %s: %w", item, err)
	}

	if err := w.prune(ctx, clone); err != nil {
		return err
	}

	return w.run(ctx, clone, gitWorktree, gitAdd, "-B", branch, dir, head)
}

// prune drops the records of the worktrees whose folder is gone, so that no such record holds a branch. A branch that a live worktree holds stays held.
func (w worktrees) prune(ctx context.Context, clone string) error {
	return w.run(ctx, clone, gitWorktree, "prune")
}

// placeKept moves the kept worktree of the item to head. The move never takes work off the worktree: changes stop it, and so do the commits of a round worktree that no remote branch holds.
func (w worktrees) placeKept(ctx context.Context, clone, dir, item, head, branch string) error {
	if err := w.refuseChanges(ctx, clone, dir, item, "the run before the fork left"); err != nil {
		return err
	}

	if _, round := model.RoundBase(item); !round {
		return w.run(ctx, dir, "checkout", "--quiet", "-B", branch, head)
	}

	if err := w.refuseUnpushed(ctx, clone, dir, item, head); err != nil {
		return err
	}

	return w.moveTo(ctx, dir, head)
}

// refuseChanges fails when the worktree has tracked or untracked changes. left names who left them.
func (w worktrees) refuseChanges(ctx context.Context, clone, dir, item, left string) error {
	status := w.git(dir).Run(ctx, []string{"status", "--porcelain"})
	if err := gitExit("status", dir, status); err != nil {
		return err
	}

	if strings.TrimSpace(status.Stdout) != "" {
		return keptTree(clone, dir, item, "changes that "+left+". Save what you need")
	}

	return nil
}

// moveTo checks the worktree out detached at revision and marks that commit as the one that beehave placed the worktree at.
func (w worktrees) moveTo(ctx context.Context, dir, revision string) error {
	if err := w.run(ctx, dir, "checkout", "--quiet", "--detach", revision); err != nil {
		return err
	}

	return w.markPlaced(ctx, dir)
}

// markPlaced marks the commit at HEAD of the worktree as the one that beehave placed it at.
func (w worktrees) markPlaced(ctx context.Context, dir string) error {
	return w.run(ctx, dir, "update-ref", placedMark, "HEAD")
}

// refuseUnpushed fails when the worktree holds commits that no remote branch holds, and the error names each commit and how to remove the worktree.
// The commit that beehave placed the worktree at, and its history, are no work of the item, also after a force-push of the base took them off the remote.
// A commit that a commit in keep holds is not counted: a move back to that commit leaves it in place. With no keep, every commit counts.
func (w worktrees) refuseUnpushed(ctx context.Context, clone, dir, item string, keep ...string) error {
	args := append([]string{"log", "--format=%h %s", "HEAD", "--not"}, keep...)

	unpushed := w.git(dir).Run(ctx, append(args, "--remotes", "--glob="+placedMarks))
	if err := gitExit("log", dir, unpushed); err != nil {
		return err
	}

	if commits := strings.TrimSpace(unpushed.Stdout); commits != "" {
		return keptTree(clone, dir, item, "commits that are on no remote branch ("+strings.ReplaceAll(commits, "\n", "; ")+"). Push or save them")
	}

	return nil
}

// keptTree is the error for a kept worktree that a fresh run refuses: it names the worktree, what it holds and the command that removes it.
func keptTree(clone, dir, item, holds string) error {
	return fmt.Errorf("%w: the worktree %s of item %s holds %s, remove the worktree with git -C %s worktree remove --force %s, then run the item again",
		errKeptChanges, dir, item, holds, clone, dir)
}

// cloneLock is the file lock of the clone of each project under the folder repos. Every git worktree command on a clone runs under it, so no two of them
// race. A file lock also holds across engines that share the repos folder.
type cloneLock struct{ repos string }

// hold runs fn while it holds the lock of the clone of the project.
func (l cloneLock) hold(ctx context.Context, project string, fn func(clone string) error) (err error) {
	if err = os.MkdirAll(l.repos, folderMode); err != nil {
		return fmt.Errorf("make the folder for the clone of project %s: %w", project, err)
	}

	clone := filepath.Join(l.repos, model.SafeName(project))

	held := lockfile.Open(clone + ".lock")
	if _, err = held.TryLockContext(ctx, lockRetry); err != nil {
		return fmt.Errorf("lock the clone of project %s: %w", project, err)
	}

	defer func() { err = errors.Join(err, held.Unlock()) }()

	return fn(clone)
}

// plain reports whether the named project has no repository. Its item folder is then a plain folder that beehave makes, and git never reads it.
// A project that the finder does not know is not plain (see repositoryOf).
func (w worktrees) plain(project string) bool {
	return !repositoryOf(w.projects)(project)
}

// ensurePlain returns the plain folder of the item and makes it when it is missing. A resume finds the folder again with its files.
func (w worktrees) ensurePlain(project, item string) (string, error) {
	dir := filepath.Join(w.root, model.SafeName(project), model.SafeName(item))
	if err := os.MkdirAll(dir, folderMode); err != nil {
		return "", fmt.Errorf("make the folder of item %s: %w", item, err)
	}

	return dir, nil
}

// addLocked refreshes the clone and adds the worktree of the item while Ensure holds the lock of the clone, so the first start makes the clone and each
// later start finds it ready. The lock also covers the fetch, because two fetches into one clone race on its ref locks.
func (w worktrees) addLocked(ctx context.Context, project config.Project, clone, item, start, base string) (string, error) {
	dir := filepath.Join(w.root, model.SafeName(project.Name), model.SafeName(item))
	if err := absent(dir); err != nil {
		return "", err
	}

	revision, err := w.refresh(ctx, project, clone, item, start, base)
	if err != nil {
		return "", err
	}

	return w.add(ctx, clone, dir, item, revision)
}

// refresh makes the clone of the project if it is missing, fetches the branch of the item into it, and returns the revision to add the worktree at.
// The branch is the base of a round item, else base when it is not empty, else the base branch of the project. A base that the remote lacks gives an error that names it.
// A round item with a start fetches nothing and returns start: the clone holds the commit that the worktree was first made at.
func (w worktrees) refresh(ctx context.Context, project config.Project, clone, item, start, base string) (string, error) {
	if err := w.clone(ctx, project, clone); err != nil {
		return "", err
	}

	branch, round := model.RoundBase(item)
	if round && start != "" {
		return start, w.holds(ctx, project, clone, item, start)
	}

	if round {
		return "origin/" + branch, w.fetchBase(ctx, project, clone, branch, item)
	}

	if base == "" {
		base = project.BaseBranch
	}

	return "origin/" + base, w.fetchBranch(ctx, project, clone, base)
}

// Fetch fetches every branch of the remote of the project onto refs/remotes/origin of its clone, and the tags that point into them, as git fetch
// origin does. It runs under the lock of the clone, and the fetch from the remote runs in the push repository.
func (w worktrees) Fetch(ctx context.Context, project string) error {
	found, err := w.project(project)
	if err != nil {
		return err
	}

	return cloneLock{repos: w.repos}.hold(ctx, project, func(clone string) error { return w.fetchAll(ctx, found, clone) })
}

// fetchAll fetches every branch from the address of the project into the push repository, then copies the branches into the clone over the file
// transport. Git follows the tags that point into the branches in both fetches, as it does in git fetch origin.
func (w worktrees) fetchAll(ctx context.Context, project config.Project, clone string) error {
	remote, err := cloneSource(project.Project, w.forge)
	if err != nil {
		return err
	}

	repo, err := w.openPush(ctx, project.Name, remote)
	if err != nil {
		return err
	}

	if _, err := repo.run(ctx, "fetch", "-q", "--no-write-fetch-head", remote, "+refs/heads/*:"+remoteRefs); err != nil {
		return err
	}

	return w.run(ctx, clone, "fetch", "-q", "--no-write-fetch-head", repo.dir, "+"+remoteRefs+":"+remoteRefs)
}

// remoteRefs are the refs of the branches of the remote, in the push repository and in the clone.
const remoteRefs = "refs/remotes/origin/*"

// fetchBase fetches the base branch of a round item into the clone. A base that the remote lacks gives an error that names it.
func (w worktrees) fetchBase(ctx context.Context, project config.Project, clone, base, item string) error {
	if err := w.fetchBranch(ctx, project, clone, base); err != nil {
		return fmt.Errorf("fetch the base branch %q of round item %s. Push the base branch to the remote, then run the item again: %w", base, item, err)
	}

	return nil
}

// fetchBranch fetches branch from the address of the project into the push repository, which reads no setting of the clone config, and sets
// refs/remotes/origin/<branch> of the clone to the tip that it fetched. The clone takes the objects from the push repository over the file transport
// only, so no git call in the clone reaches the network. The caller holds the lock of the clone, which also covers the push repository.
func (w worktrees) fetchBranch(ctx context.Context, project config.Project, clone, branch string) error {
	remote, err := cloneSource(project.Project, w.forge)
	if err != nil {
		return err
	}

	ref := "refs/remotes/origin/" + branch

	dir, tip, err := w.fetchRemote(ctx, project.Name, remote, branch, ref)
	if err != nil {
		return err
	}

	if err := w.run(ctx, clone, "fetch", "-q", "--no-tags", "--no-write-fetch-head", dir, ref); err != nil {
		return err
	}

	return w.run(ctx, clone, "update-ref", ref, tip)
}

// fetchRemote fetches branch from remote onto ref of the push repository of the project, and returns the repository and the commit that ref then names.
func (w worktrees) fetchRemote(ctx context.Context, project, remote, branch, ref string) (dir, tip string, err error) {
	repo, err := w.openPush(ctx, project, remote)
	if err != nil {
		return "", "", err
	}

	if _, err := repo.run(ctx, "fetch", "-q", "--no-tags", "--no-write-fetch-head", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", "", err
	}

	tip, err = repo.run(ctx, "rev-parse", "--verify", ref+"^{commit}")

	return repo.dir, strings.TrimSpace(tip), err
}

// Heads returns the lines of git ls-remote --heads for the address of the project, which the engine reads from the project file or the forge. The call
// runs in the push repository of the project, so no setting of the clone config reaches it. The caller holds the lock of the clone, as WithTree does.
func (w worktrees) Heads(ctx context.Context, project string) (string, error) {
	found, err := w.project(project)
	if err != nil {
		return "", err
	}

	remote, err := cloneSource(found.Project, w.forge)
	if err != nil {
		return "", err
	}

	repo, err := w.openPush(ctx, project, remote)
	if err != nil {
		return "", err
	}

	return repo.run(ctx, "ls-remote", "--heads", remote)
}

// BaseTip reads the commit that branch names at remote, the address of the project, with git ls-remote in the push repository. The runner is the one
// that reach picks, so no setting of the clone config reaches the read, and a project with credentials reads as its writer.
// The read holds no lock, so the gates of one project do not wait on each other for the latency of the source.
func (w worktrees) BaseTip(ctx context.Context, project, remote, branch string) (string, error) {
	repo, err := w.openPushLocked(ctx, project, remote)
	if err != nil {
		return "", err
	}

	listing, err := repo.run(ctx, "ls-remote", "--", remote, gitexec.TipRef(branch))
	if err != nil {
		return "", err
	}

	return gitexec.TipOf(listing, remote, branch)
}

// FetchCommit fetches the commit sha by its id from remote into the push repository, then copies it into the clone over the file transport, as
// fetchBranch does. A source that speaks protocol version 2 serves any commit it holds by its id.
func (w worktrees) FetchCommit(ctx context.Context, project, remote, sha string) error {
	return w.inPush(ctx, project, remote, func(clone string, repo pushRepo) error {
		if _, err := repo.run(ctx, "fetch", "-q", "--no-tags", "--no-write-fetch-head", "--", remote, sha); err != nil {
			return fmt.Errorf("a fetch by id needs a source that serves a commit by its id (git protocol version 2); if the base moved since the read, run again: %w", err)
		}

		return w.run(ctx, clone, "fetch", "-q", "--no-tags", "--no-write-fetch-head", repo.dir, sha)
	})
}

// inPush runs fn with the clone and the push repository of the project, under the lock of the clone.
func (w worktrees) inPush(ctx context.Context, project, remote string, fn func(clone string, repo pushRepo) error) error {
	return cloneLock{repos: w.repos}.hold(ctx, project, func(clone string) error {
		repo, err := w.openPush(ctx, project, remote)
		if err != nil {
			return err
		}

		return fn(clone, repo)
	})
}

// openPushLocked makes the push repository of the project under the lock of the clone, and gives the lock back before the caller reaches the remote.
// A call that only reads the remote does not touch the clone or the objects of the push repository, so it needs no lock.
func (w worktrees) openPushLocked(ctx context.Context, project, remote string) (repo pushRepo, err error) {
	err = cloneLock{repos: w.repos}.hold(ctx, project, func(string) error {
		repo, err = w.openPush(ctx, project, remote)

		return err
	})

	return repo, err
}

// pushRepo is the push repository dir of a project, with the git that runs the calls that reach the remote.
type pushRepo struct {
	git proc.Runner
	dir string
}

// openPush makes the push repository of the project when it is missing and returns it. The caller holds the lock of the clone, which also covers the
// push repository.
func (w worktrees) openPush(ctx context.Context, project, remote string) (pushRepo, error) {
	root, dir := pushRepository(w.repos, project)
	repo := pushRepo{git: checkedPush{git: w.reach(project, dir, remote), dir: dir, check: w.pushCheck}, dir: dir}

	return repo, openPushRepository(ctx, repo.git, root, dir)
}

// run runs git with args in the repository and returns what git printed. --git-dir names the repository, so git reads no other repository and its config.
func (p pushRepo) run(ctx context.Context, args ...string) (string, error) {
	out := p.git.Run(ctx, append([]string{"--git-dir=" + p.dir}, args...))

	return out.Stdout, gitExit(args[0], p.dir, out)
}

// holds checks that the clone has the commit that the worktree of a round item was first made at. A clone that was made again, or a commit that git pruned
// after a push rewrote the base, gives an error that says so and how to go on.
func (w worktrees) holds(ctx context.Context, project config.Project, clone, item, start string) error {
	if err := w.run(ctx, clone, "cat-file", "-e", start+"^{commit}"); err != nil {
		return fmt.Errorf("the clone of project %s does not hold the commit %s that the worktree of item %s was made at, so the worktree cannot be made again. "+
			"End the failed run first: beehave cancel %s. Start the item again: beehave run %s: %w", project.Name, start, item, item, item, err)
	}

	return nil
}

// project returns the config of the named project.
func (w worktrees) project(name string) (config.Project, error) {
	found, ok := w.projects[name]
	if !ok {
		return config.Project{}, fmt.Errorf("%w: %q", errUnknownProject, name)
	}

	return found, nil
}

// absent fails if dir exists. addLocked calls it for a folder that the lookup did not find, so git does not list the folder.
func absent(dir string) error {
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%w: %s. Move it away, then run the item again", errNotListed, dir)
	}

	return nil
}

// add makes the worktree dir from the clone. A round item gets a detached worktree at revision, with --force, so git replaces the record of a round
// worktree whose folder someone removed and forgets no other record. Any other item first drops the records of removed folders, then uses the branch
// named item if the clone has one, and else starts that branch at revision. It returns dir.
func (w worktrees) add(ctx context.Context, clone, dir, item, revision string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), folderMode); err != nil {
		return "", fmt.Errorf("make the folder for the worktree of item %s: %w", item, err)
	}

	args, err := w.addArgs(ctx, clone, dir, item, revision)
	if err != nil {
		return "", err
	}

	if err := w.run(ctx, clone, args...); err != nil {
		return "", err
	}

	if _, round := model.RoundBase(item); round {
		if err := w.markPlaced(ctx, dir); err != nil {
			return "", err
		}
	}

	return dir, nil
}

// addArgs returns the git arguments that add the worktree dir of the item at revision. An existing branch of the item is added after a prune, so the record of a removed folder does not hold it.
func (w worktrees) addArgs(ctx context.Context, clone, dir, item, revision string) ([]string, error) {
	if _, round := model.RoundBase(item); round {
		return []string{gitWorktree, gitAdd, "--force", "--detach", dir, revision}, nil
	}

	branch := model.ItemBranch(item)
	if w.git(clone).Run(ctx, []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + branch}).ExitCode == 0 {
		return []string{gitWorktree, gitAdd, dir, branch}, w.prune(ctx, clone)
	}

	return []string{gitWorktree, gitAdd, "-b", branch, dir, revision}, nil
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

	return gitExit("clone", w.repos, w.reach(project.Name, w.repos, remote).Run(ctx, []string{"clone", remote, dir}))
}

// reach returns the git runner for a call in dir that reaches the address remote of the project. Every call of the worktrees to a remote picks its runner
// here, so the engine and the client both run it as the writer of a project that has credentials (see writers.git).
func (w worktrees) reach(project, dir, remote string) proc.Runner { //nolint:ireturn // both runners are the interface of the worktrees
	return w.roles.git(w.projects, project, dir, remote, w.remote, []string{noPromptEnv})
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
	return gitExit(args[0], dir, out)
}

// gitExit returns nil when git exited with code 0, and an error that carries what git printed when it did not.
func gitExit(name, dir string, out model.CommandResult) error {
	if out.ExitCode != 0 {
		return fmt.Errorf("git %s in %s exited with code %d: %s", name, dir, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	return nil
}

// Git returns the runner of the engine for git in a clone or a worktree, pinned to the repository that the records of the clones name.
func (w worktrees) Git(dir string) proc.Runner { //nolint:ireturn // the trees contract of the engine returns the interface
	return w.git(dir)
}

// Path returns the worktree of the item, or an empty path when the item has none. The git call that lists the worktrees runs under the lock of the clone.
func (w worktrees) Path(ctx context.Context, project, item string) (string, error) {
	var found string

	if err := w.WithTree(ctx, project, item, func(dir string) { found = dir }); err != nil {
		return "", err
	}

	return found, nil
}

// WithTree runs fn with the worktree of the item, or with an empty path when the item has none. The lookup and fn run under the lock of the clone, so
// the release can remove the worktree while no sibling item adds or lists one.
func (w worktrees) WithTree(ctx context.Context, project, item string, fn func(dir string)) error {
	dir, want, err := w.folder(project, item)
	if err != nil {
		return err
	}

	if dir == "" || w.plain(project) {
		fn(dir)

		return nil
	}

	return cloneLock{repos: w.repos}.hold(ctx, project, func(string) error { return w.withListed(ctx, dir, want, fn) })
}

// withListed runs fn with dir when git lists the folder want as a worktree, and with an empty path when it does not.
func (w worktrees) withListed(ctx context.Context, dir, want string, fn func(dir string)) error {
	found, err := w.ifListed(ctx, dir, want)
	if err != nil {
		return err
	}

	fn(found)

	return nil
}

// lookup is Path for a caller that holds the lock of the clone.
func (w worktrees) lookup(ctx context.Context, project, item string) (string, error) {
	dir, want, err := w.folder(project, item)
	if err != nil || dir == "" {
		return dir, err
	}

	return w.ifListed(ctx, dir, want)
}

// folder returns the item folder and its real path, or empty paths when the folder does not exist.
func (w worktrees) folder(project, item string) (dir, resolved string, err error) {
	dir = filepath.Join(w.root, model.SafeName(project), model.SafeName(item))

	resolved, err = filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	} else if err != nil {
		return "", "", fmt.Errorf("read the folder %s: %w", dir, err)
	}

	return dir, resolved, nil
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
	if err := gitExit("worktree list", dir, out); err != nil {
		return nil, err
	}

	return worktreePaths(out.Stdout), nil
}

// worktreePaths returns the path of each worktree in the output of git worktree list --porcelain.
func worktreePaths(listing string) []string {
	var paths []string

	for line := range strings.Lines(listing) {
		if path, found := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "worktree "); found {
			paths = append(paths, path)
		}
	}

	return paths
}

// treeFinder is what the command runner needs of the worktrees.
type treeFinder interface {
	Path(ctx context.Context, project, item string) (string, error)
}

// programTrees is what the command runner needs of the worktrees: it finds the tree of an item, and makes the tree of a round item again at its start commit when it is gone.
type programTrees interface {
	treeFinder
	Ensure(ctx context.Context, project, item, start, base string) (string, error)
}

// programs runs the programs of command steps. A step with no host runs in the worktree of the item, or in a new empty folder that beehave removes afterwards when the item has none.
// A step with a host runs as one job on the host of the project. A host job that succeeds has no output to read. A host job that fails has the end of its log as stdout, and its code is 1.
type programs struct {
	trees programTrees
	// local runs a program of a project with the environment of the process. The gate uses it.
	local func(project, binary, dir string, timeout time.Duration) proc.Runner
	// exec runs the program of a command step. Its spec holds the whole environment of the program (Only) and its stdin.
	exec func(spec proc.ExecRunner) proc.Runner
	// hosts builds the host of a cluster of the project. An empty cluster is the build host of the project.
	hosts func(project, cluster string) (host.Host, error)
	// declared resolves the cluster a run chose to a cluster the project declares: an empty choice gives the build host, an undeclared one is refused.
	// A nil declared passes the choice through.
	declared func(project, cluster string) (string, error)
	// sandbox runs a gate under the sandbox of its project.
	sandbox gateSandbox
}

// git runs git of the project with args in dir for at most timeout and returns what it printed, without the space at both ends.
// When git fails, the error starts with failure and ends with what git printed on stderr.
func (p programs) git(ctx context.Context, project, dir string, timeout time.Duration, failure string, args ...string) (string, error) {
	out := p.local(project, "git", dir, timeout).Run(ctx, args)
	if out.ExitCode != 0 {
		return "", fmt.Errorf("%s in %s: %s", failure, dir, strings.TrimSpace(out.Stderr))
	}

	return strings.TrimSpace(out.Stdout), nil
}

// Run runs the program of the request and returns what it printed and its exit code.
func (p programs) Run(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	if len(request.Run) == 0 {
		return flow.CommandOutput{}, errEmptyCommand
	}

	if request.Host != "" {
		return p.onHost(ctx, request)
	}

	dir, err := p.folder(ctx, request)
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("find the folder of item %s: %w", request.Item, err)
	}

	if request.Sandbox != nil || (request.Gate && p.sandbox.applies(request.Project, true)) {
		return p.sandboxed(ctx, request, dir)
	}

	return p.inFolder(ctx, request, dir)
}

// folder returns the worktree of the item of the request. A request with a tree start is of a round item, whose worktree someone may have removed:
// it is made again at that commit. Any other item may have no worktree.
func (p programs) folder(ctx context.Context, request flow.CommandRequest) (string, error) {
	if request.TreeStart != "" {
		return p.trees.Ensure(ctx, request.Project, request.Item, request.TreeStart, "")
	}

	return p.trees.Path(ctx, request.Project, request.Item)
}

// sandboxed runs the program of a gate, or of a command call with the sandbox keyword, under the sandbox of its project. A command call with the keyword
// reaches the domains of its own network list in place of gate_network. Stdin and the process cap of the request do not apply to a sandboxed program.
// The output is the plan, then what the program printed, with stdout and stderr in one stream.
func (p programs) sandboxed(ctx context.Context, request flow.CommandRequest, dir string) (flow.CommandOutput, error) {
	own := dir == ""

	dir, cleanup, err := commandFolder(dir, request.Item)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	defer cleanup()

	box := p.sandbox.asked(request.Project, request.Sandbox)

	run := box.run
	if own {
		run = box.runOwn
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

	return fmt.Sprintf("\nbeehave: a process start failed. The command ran with max_new_processes = %d: %s.\n", limit, fmt.Sprintf(proc.CapMeaning, limit))
}

// commandFolder returns dir, or a new empty folder when dir is empty because the item has no worktree. The program then never touches the folder where beehave started.
// The returned function removes a new folder, and does nothing for dir.
func commandFolder(dir, item string) (string, func(), error) {
	if dir != "" {
		return dir, func() {}, nil
	}

	scratch, err := os.MkdirTemp("", "beehave-command-")
	if err != nil {
		return "", nil, fmt.Errorf("make an empty folder for the command of item %s: %w", item, err)
	}

	return scratch, func() { _ = os.RemoveAll(scratch) }, nil
}

func (p programs) onHost(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	h, err := p.hosts(request.Project, request.BuildHost)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	id, err := h.Submit(ctx, model.JobSpec{Name: "beehave-" + request.Item, Command: shellLine(request.Run), Key: host.JobKey(ctx), Slot: model.Slot{Class: request.SlotClass, Count: request.SlotCount}})
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("submit the command of item %s to the host: %w", request.Item, err)
	}

	state, err := host.WaitJob(ctx, h, id)
	if err != nil {
		return flow.CommandOutput{}, fmt.Errorf("wait for the command of item %s on the host: %w", request.Item, err)
	}

	return withLog(ctx, h, id, state)
}

// withLog maps the end state of a host job as hostOutput does. A failed job also gives the end of its log as stdout, so a workflow reads what the program printed.
// A log that cannot be read leaves stdout empty: the exit code still says the job failed.
func withLog(ctx context.Context, h host.Host, id model.JobID, state model.JobState) (flow.CommandOutput, error) {
	out, err := hostOutput(state)
	if err != nil || state != model.JobFailed {
		return out, err
	}

	if log, logErr := h.Log(ctx, id); logErr == nil {
		out.Stdout = log
	}

	return out, nil
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
	case model.JobDiskFull:
		return "the job was stopped because the host ran under its free-space floor `host.disk_floor_gb`. Free disk space on the host, then run it again"
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
		quoted[i] = model.ShellQuote(word)
	}

	return strings.Join(quoted, " ")
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
// trees finds the clone that a push copies the head from, and copier runs the copy.
type pulls struct {
	projects map[string]config.Project
	forge    func(model.Project) (forge.Forge, error)
	runners  []string
	trees    worktrees
	copier   headCopy
	// writers gives the push of a project the writer role of that project. A project with none pushes with the login of the owner.
	writers *writers
}

// opened is the forge of one project. It passes the optional reads of the merge (trees and failed logs) to the forge when the forge has them:
// an embedded interface hides every method that it does not list, so the merge would not see them without these methods.
type opened struct{ forge.Forge }

var errNoOptionalRead = errors.New("this forge cannot read it")

// IssueAssignees reads who holds an issue, when the forge can. A forge that cannot is an error, so preflight never reads it as an issue nobody holds.
func (o opened) IssueAssignees(ctx context.Context, repo string, number int) ([]string, error) {
	reads, ok := o.Forge.(forge.AssigneeReads)
	if !ok {
		return nil, fmt.Errorf("%w: the assignees of an issue", errNoOptionalRead)
	}

	return reads.IssueAssignees(ctx, repo, number)
}

// ReadyMarks reads who marked a pull request ready for review, when the forge can. A forge that cannot is an error, so the poll never reads it as a pull request that nobody marked.
func (o opened) ReadyMarks(ctx context.Context, pr int) ([]string, error) {
	reads, ok := o.Forge.(forge.ReadyReads)
	if !ok {
		return nil, fmt.Errorf("%w: the ready marks of a pull request", errNoOptionalRead)
	}

	return reads.ReadyMarks(ctx, pr)
}

// AutoMergePR queues the merge of a pull request on the forge, when the forge can. A forge that cannot is an error.
func (o opened) AutoMergePR(ctx context.Context, pr int, sha string) error {
	queue, ok := o.Forge.(merge.Queuer)
	if !ok {
		return fmt.Errorf("%w: a queued merge", errNoOptionalRead)
	}

	return queue.AutoMergePR(ctx, pr, sha)
}

// Protection reads what the forge requires of a branch, when the forge can. A forge that cannot returns merge.ErrNoProtectionRead.
func (o opened) Protection(ctx context.Context, branch string) (model.Protection, error) {
	reads, ok := o.Forge.(forge.Statuses)
	if !ok {
		return model.Protection{}, merge.ErrNoProtectionRead
	}

	return reads.Protection(ctx, branch)
}

// DisableAutoMergePR turns off the queued merge of a pull request, when the forge can queue one. A forge that cannot is an error.
func (o opened) DisableAutoMergePR(ctx context.Context, pr int) error {
	queue, ok := o.Forge.(merge.Queuer)
	if !ok {
		return fmt.Errorf("%w: a queued merge", errNoOptionalRead)
	}

	return queue.DisableAutoMergePR(ctx, pr)
}

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

// Compare reads how the forge compares a commit with the base: the status and both counts. It needs a forge that compares commits.
func (o opened) Compare(ctx context.Context, commit, base string) (model.Comparison, error) {
	history, ok := o.Forge.(merge.Ancestry)
	if !ok {
		return model.Comparison{}, fmt.Errorf("%w: ancestry", errNoOptionalRead)
	}

	return history.Compare(ctx, commit, base)
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

// projectOf returns the named project. A name that the config lacks is an error, so no request is built for a project that beehave does not know.
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

	return built, 0, fmt.Errorf("%w: no pull request is on the branch %s", forge.ErrNoPull, item)
}

// listed returns the number of the one pull request in the state on the branch of the item, or 0 when there is none.
func listed(ctx context.Context, built forge.Forge, item, state string) (int, error) {
	rows, err := built.PRList(ctx, pullListLimit, "--head", model.ItemBranch(item), "--state", state, "--json", "number")
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

// ClosingIssues reads the issues that merging the pull request of the item closes, as the forge lists them: the issues of the repository of the pull request as "#N",
// and the issues of other repositories as "owner/repo#N". An item with no pull request gives found false: there is no scope to fix yet.
// A project with no forge is an error that matches forge.ErrNoForge.
func (p pulls) ClosingIssues(ctx context.Context, project, item string) (issues []model.IssueRef, found bool, err error) {
	built, number, err := p.find(ctx, project, item)
	if errors.Is(err, forge.ErrNoPull) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	refs, err := built.ClosingRefs(ctx, number)
	if err != nil {
		return nil, false, fmt.Errorf("read the issues that pull request %d closes: %w", number, err)
	}

	for _, number := range refs.Issues {
		issues = append(issues, model.OwnIssue(number))
	}

	return append(issues, refs.Elsewhere...), true, nil
}

// Comment adds a comment to the pull request of the item, once for each key. A project with no forge and an item with no pull request are errors.
func (p pulls) Comment(ctx context.Context, project, item, key, text string) (int, error) {
	built, number, err := p.find(ctx, project, item)
	if err != nil {
		return 0, err
	}

	return number, forge.PostOnce(ctx, built, number, key, text)
}

// Merge merges the pull request of the item at head, through the guards of the merge package and the settings of the merge step.
// The runners of the config count as own identities, next to the logins and addresses of the step. A pattern that is not RE2 is an error.
func (p pulls) Merge(ctx context.Context, project, item, head string, settings flow.MergeSettings) (merge.Result, error) {
	return p.judged(ctx, project, item, head, settings, merge.Run)
}

// Poll reads the queued merge of the pull request of the item once, with the same request as Merge. Last is true for the final read of the wait.
func (p pulls) Poll(ctx context.Context, project, item, head string, settings flow.MergeSettings, last bool) (merge.Result, error) {
	return p.judged(ctx, project, item, head, settings, func(ctx context.Context, f merge.Forge, req merge.Request) merge.Result {
		return merge.Poll(ctx, f, req, last)
	})
}

// Disarm turns off the queued merge of the pull request of the item, with the same request as Merge. A merge that may still be armed is an error whose text is the lines of the refusal.
func (p pulls) Disarm(ctx context.Context, project, item, head string, settings flow.MergeSettings) error {
	result, err := p.judged(ctx, project, item, head, settings, merge.Disarm)
	if err != nil {
		return err
	}

	if result.Stop != merge.Done {
		return errors.New(strings.Join(result.Lines, "; "))
	}

	return nil
}

// judged builds the merge request of the item and passes it to judge.
func (p pulls) judged(ctx context.Context, project, item, head string, settings flow.MergeSettings, judge func(context.Context, merge.Forge, merge.Request) merge.Result) (merge.Result, error) {
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

	req := p.request(project, found, item, number, head, settings, rules)
	req.Post = posting(found.Project, built.Forge, settings.Statuses)

	return judge(ctx, built, req), nil
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

// ReadPull reads the pull request of the item, found as Preflight finds it. An item with no pull request in any state gives facts with number 0.
func (p pulls) ReadPull(ctx context.Context, project, item string) (model.PullFacts, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return model.PullFacts{}, err
	}

	number, err := anyState(ctx, built, item)
	if err != nil || number == 0 {
		return model.PullFacts{}, err
	}

	return built.PullRead(ctx, number)
}

// IssueAssignees reads who holds an issue of the project, on its own repository when repo names one.
func (p pulls) IssueAssignees(ctx context.Context, project, repo string, number int) ([]string, error) {
	built, err := p.forgeOf(project)
	if err != nil {
		return nil, err
	}

	return built.IssueAssignees(ctx, repo, number)
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
	rows, err := built.PRList(ctx, pullListLimit, "--head", model.ItemBranch(item), "--state", state, "--json", "number")
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
// and the request carries the line that says so. The caller passes the project it has already found, so a project that beehave does not know never gets here.
func (p pulls) request(name string, project config.Project, item string, number int, head string, settings flow.MergeSettings, rules []*regexp.Regexp) merge.Request {
	names, unaudited := audited(name, project.Tracker, item)

	return merge.Request{
		AuditClosing: names != nil, Names: names, Kept: settings.Kept, Unaudited: unaudited,
		PR: number, Gated: head, Repo: settings.Repo, Base: settings.Base, Runners: p.runners,
		OwnLogins: settings.Logins, OwnEmails: settings.Emails, Approved: settings.Approved, OwnDraft: settings.OwnDraft,
		Hold: settings.Hold, ForbidText: rules, Retarget: settings.Retarget, GatedTree: settings.GatedTree, Intent: settings.Intent,
		Queue: true, Credentialed: project.HasWriter(), ForgeName: string(project.Forge),
	}
}

// prepared returns the known project and the compiled forbid_text patterns of a merge step.
func (p pulls) prepared(project string, patterns []string) (config.Project, []*regexp.Regexp, error) {
	rules, err := merge.Compile(patterns)
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

	lines := []string{wrong.Error()}

	return merge.Result{Stop: merge.Refused, Lines: lines, Refusal: lines}, true
}

// scopeStillAsks ends the line of an item with no audit: the scope check of the run does not read the item id, so it still holds.
const scopeStillAsks = "The scope check of the run still asks the owner about a closing issue that is new since the first review, in this repository or another."

// audited returns the issues that the item names, or the line that says why the audit is off.
func audited(project string, tracker model.TrackerName, item string) ([]int, string) {
	named, err := strconv.Atoi(item)

	switch {
	case tracker != storegithub.Tracker:
		return nil, fmt.Sprintf("closing references: the tracker %s of project %s has no issue numbers, so no audit ran. %s", tracker, project, scopeStillAsks)
	case err != nil || named <= 0 || strconv.Itoa(named) != item:
		return nil, fmt.Sprintf("closing references: the item id %q of project %s is not a plain issue number, so no audit ran. %s", item, project, scopeStillAsks)
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
func ownerToken() string { return "beehave" + rand.Text() }

// itemLife is the lifecycle of an item: the claim from the claim store and the pull request from the forge.
type itemLife struct {
	leases
	pulls
}
