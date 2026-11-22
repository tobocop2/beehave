package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehave/internal/fstree"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/sandbox"
)

var errNoCloneToPush = errors.New("the project has no clone to push from")

// Push pushes head to branch on the remote of the project, from the engine process. The head is first copied from the clone into the push repository of
// the project, which only the engine writes, by git upload-pack under the sandbox enforcer of the project with read on the clone folder and the system folders. The push then
// runs in that repository, with the hooks off, to the address that the engine read from the project file or the forge, never a remote name.
// With expect, the push carries a lease on the branch; with no expect, git takes only a new branch or a fast forward. Git checks no lease when the branch
// is already at head and exits 0, so a replay after the push landed passes. A project with no forge is an error, as for Open: such a project pushes nothing.
func (p pulls) Push(ctx context.Context, project, item, branch, head, expect string) error {
	remote, err := p.pushRemote(project)
	if err != nil {
		return err
	}

	dir, err := p.copyHead(ctx, project, branch, head)
	if err != nil {
		return fmt.Errorf("push %s to the branch %s for item %s: %w", head, branch, item, err)
	}

	out := p.pusher(project, dir).Run(ctx, pushArgs(remote, branch, head, expect))
	if out.ExitCode == 0 {
		return nil
	}

	return fmt.Errorf("push %s to the branch %s of %s for item %s: git exited with code %d: %s. Read the branch on the remote: a branch that moved since the last push of the run stops the item",
		head, branch, remote, item, out.ExitCode, strings.TrimSpace(out.Stderr))
}

// pusher returns the git runner that pushes from the push repository dir: the writer of the project when it has credentials, else the runner of the owner.
func (p pulls) pusher(project, dir string) proc.Runner { //nolint:ireturn // both runners are the interface of the push
	if runner, found := p.writers.pushRunner(project, dir); found {
		return runner
	}

	return pushGit(dir)
}

// pushRemote returns the address that the clone of the project came from. A project with no forge is an error.
func (p pulls) pushRemote(project string) (string, error) {
	// forgeOf refuses a project that the config lacks, so the lookup below finds it.
	if _, err := p.forgeOf(project); err != nil {
		return "", err
	}

	return cloneSource(p.projects[project].Project, p.forge)
}

// headCopy is what the copy of a head needs: the enforcer that a sandbox_enforcer value names, and the path of this program, which git runs as upload-pack.
type headCopy struct {
	enforcer   func(name string) (sandbox.Enforcer, error)
	executable func() (string, error)
}

// newHeadCopy returns the copy with the enforcers of this machine and this program.
func newHeadCopy() headCopy { return headCopy{enforcer: chooseEnforcer, executable: os.Executable} }

// copyHead fetches head from the clone of the project onto refs/heads/branch of the push repository of the project, under the lock of the clone, and
// returns the repository. The repository is made on the first copy and kept, so a later copy fetches only the new objects.
func (p pulls) copyHead(ctx context.Context, project, branch, head string) (string, error) {
	folder, program, err := p.copySource(project)
	if err != nil {
		return "", err
	}

	root, dir := pushRepository(p.trees.repos, project)

	err = cloneLock{repos: p.trees.repos}.hold(ctx, project, func(string) error { return fetchHead(ctx, root, dir, folder, program, branch, head) })

	return dir, err
}

// copySource returns the clone folder of the project with its links resolved, and the upload-pack program that reads it under the enforcer of the project.
// A machine where no enforcer runs, and a project with no clone, are errors.
func (p pulls) copySource(project string) (folder, program string, err error) {
	enforcer, err := p.copier.enforcer(p.projects[project].SandboxEnforcer)
	if err != nil {
		return "", "", fmt.Errorf("the push copies the head from the clone under the sandbox, and %w. Set sandbox_enforcer to an enforcer that runs on this machine", err)
	}

	folder, err = filepath.EvalSymlinks(filepath.Join(p.trees.repos, model.SafeName(project)))
	if err != nil {
		return "", "", fmt.Errorf("%w: project %s: %w", errNoCloneToPush, project, err)
	}

	self, err := p.copier.executable()
	if err != nil {
		return "", "", fmt.Errorf("find the beehave program, which copies the head from the clone: %w", err)
	}

	return folder, uploadPackProgram(self, enforcer.Name(), folder), nil
}

// pushRepository returns the owner-only folder of the push repositories, beside the repos folder, and the push repository of the project in it.
func pushRepository(repos, project string) (root, dir string) {
	root = filepath.Join(filepath.Dir(repos), "push")

	return root, filepath.Join(root, model.SafeName(project)+".git")
}

// openPushRepository makes the push repository dir in the owner-only folder root with git when it is missing, with no template and so no hooks.
func openPushRepository(ctx context.Context, git proc.Runner, root, dir string) error {
	if err := os.MkdirAll(dir, ownerOnly); err != nil {
		return fmt.Errorf("make the push repository: %w", err)
	}

	if err := fstree.TrustedRoot(root, os.Getuid()); err != nil {
		return fmt.Errorf("the folder of the push repositories: %w", err)
	}

	return gitExit("init", dir, git.Run(ctx, []string{"init", "-q", "--bare", "--template=", dir}))
}

// fetchHead opens the push repository dir in root and fetches head from the clone folder onto refs/heads/branch with program as git upload-pack.
func fetchHead(ctx context.Context, root, dir, folder, program, branch, head string) error {
	if err := openPushRepository(ctx, pushGit(dir), root, dir); err != nil {
		return err
	}

	out := pushGit(dir).Run(ctx, []string{"fetch", "-q", "--no-tags", "--no-write-fetch-head", "--upload-pack=" + program, folder, "+" + head + ":refs/heads/" + branch})
	if out.ExitCode != 0 {
		return fmt.Errorf("copy %s from the clone: git exited with code %d: %s. The copy reads only the clone folder, so a head that needs an object from outside the clone, "+
			"through a list of other object stores or a link, is refused", head, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	return nil
}

// pushGit runs git in the push repository dir, with the hooks off and no prompt for a password. GIT_DIR names the repository, so git does not look for
// one in a parent folder. Only the engine writes the push repository, so its runner reaches the remote.
func pushGit(dir string) proc.Runner { //nolint:ireturn // the runner of the worktrees is the same interface
	runner := gitexec.NewRemote(dir, proc.DefaultTimeout)
	runner.Env = []string{"GIT_TERMINAL_PROMPT=0", "GIT_DIR=" + dir}

	return runner
}

// pushArgs returns the git arguments that push head to the branch at remote: with --force-with-lease=<ref>:<expect> when expect is set, and as a plain
// push, which takes only a new branch or a fast forward, when it is empty.
func pushArgs(remote, branch, head, expect string) []string {
	ref := "refs/heads/" + branch

	args := []string{"push", "--no-verify"}
	if expect != "" {
		args = append(args, "--force-with-lease="+ref+":"+expect)
	}

	return append(args, remote, head+":"+ref)
}
