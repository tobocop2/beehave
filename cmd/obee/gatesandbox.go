package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/sandbox"
	"github.com/tobocop2/obee/internal/sandbox/enforcers"
)

// sandboxFailed is the exit code of a gate that the sandbox could not start. The gate command ran no code.
const sandboxFailed = 1

// gateSandbox runs the gate commands of the projects that have their sandbox on and build here under the enforcer that the project asks for.
type gateSandbox struct {
	projects map[string]config.Project
	// enforcer returns the enforcer for the sandbox_enforcer value of a project.
	enforcer func(name string) (sandbox.Enforcer, error)
	home     func() (string, error)
	// environ is the environment of a gate command whose request holds none.
	environ []string
}

func newGateSandbox(projects map[string]config.Project) gateSandbox {
	choose := func(name string) (sandbox.Enforcer, error) {
		table := enforcers.Table()

		key, err := sandbox.Choose(table, enforcers.Order(), name)
		if err != nil {
			return nil, err
		}

		return table[key], nil
	}

	return gateSandbox{projects: projects, enforcer: choose, home: os.UserHomeDir, environ: environList(config.ProcessEnv())}
}

// environList turns an environment map into NAME=value pairs in name order.
func environList(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for name, value := range env {
		pairs = append(pairs, name+"="+value)
	}

	slices.Sort(pairs)

	return pairs
}

// applies says whether a gate of the project runs under the sandbox on this machine: the project has the sandbox on and the gate runs here.
func (g gateSandbox) applies(project string, program bool) bool {
	return config.GateRefused(g.projects[project].Project, program)
}

// run runs argv in dir under the plan of the project and returns what it printed and its exit code. The first line of the output is the plan,
// so the report of the gate step records it. The gate command ran no code when the code is sandboxFailed and the first words are "obee sandbox".
func (g gateSandbox) run(ctx context.Context, project, dir string, argv, env []string, limit time.Duration) (out string, code int) {
	found := g.projects[project]

	enforcer, err := g.enforcer(found.SandboxEnforcer)
	if err != nil {
		return notStarted(err.Error())
	}

	tmp, err := gateTemp()
	if err != nil {
		return notStarted(fmt.Sprintf("make the temporary folder: %v", err))
	}

	defer func() { out += discard(context.WithoutCancel(ctx), tmp, dir) }()

	return g.runIn(ctx, enforcer, found, tmp, dir, argv, env, limit)
}

// discard removes the copy of a gate, also when the context is done, and then tells the repository in dir to forget the records of gate copies whose folder
// is gone. That covers the record that the gate could not name. It returns a line for the output when the copy stays.
func discard(ctx context.Context, tmp, dir string) string {
	note := ""
	if err := removeTree(tmp); err != nil {
		note = fmt.Sprintf("obee sandbox: the copy of the gate stays at %s: %v\n", tmp, err)
	}

	forgetCopies(ctx, dir, filepath.Dir(tmp))

	return note
}

// ownerOnly is the mode of the folders that obee makes for gate copies.
const ownerOnly = 0o700

// gateTemp makes the temporary folder of one gate under the one folder that obee owns for them. It first removes the folders of gates whose process is
// gone, so a gate that was killed leaves a copy for the next gate to reclaim, not for the owner to find.
func gateTemp() (string, error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("obee-gate-%d", os.Getuid()))

	if err := os.Mkdir(root, ownerOnly); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}

	if err := trustedRoot(root, os.Getuid()); err != nil {
		return "", err
	}

	if err := reclaim(root); err != nil {
		return "", err
	}

	return os.MkdirTemp(root, strconv.Itoa(os.Getpid())+"-")
}

// trustedRoot looks at the folder of gates itself, not at what a link there points to, and applies trusted.
func trustedRoot(root string, uid int) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}

	return trusted(root, info, uid)
}

// trusted refuses a folder of gates that another user could have made or can change. On a shared temporary folder another user can make the fixed name
// first, and then swap a gate folder for a link, so the write rule of the plan would land on the target of the link. The folder must be a real folder
// (not a link) that the user owns, and no group or other user may write it.
func trusted(root string, info fs.FileInfo, uid int) error {
	owner, owned := info.Sys().(*syscall.Stat_t)

	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a folder that obee made", root)
	case !owned || int(owner.Uid) != uid:
		return fmt.Errorf("%s belongs to another user", root)
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s can be written by another user", root)
	}

	return nil
}

// reclaim removes the folders in root that belong to a process which is gone. A folder that stays is tried again by the next gate.
func reclaim(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !ownerAlive(entry.Name()) {
			_ = removeTree(filepath.Join(root, entry.Name())) //nolint:errcheck // best effort; the next gate tries again
		}
	}

	return nil
}

// ownerAlive says whether the process that the name of a gate folder (<pid>-<random>) holds is still running. A name that holds no process id is not alive.
func ownerAlive(name string) bool {
	text, _, _ := strings.Cut(name, "-")

	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return false
	}

	err = syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

// removeTree removes path with all it holds. When that fails, it gives the owner write and search rights on every folder and tries again, so a gate that
// left a read-only folder (a module cache is extracted read-only) does not keep the copy.
func removeTree(path string) error {
	if os.RemoveAll(path) == nil {
		return nil
	}

	//nolint:errcheck // a folder that cannot change mode makes the second removal fail, and that failure is returned
	_ = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}

		return os.Chmod(p, ownerOnly) //nolint:gosec // the folder is inside a copy that obee removes
	})

	return os.RemoveAll(path)
}

// forgetCopies tells the repository in dir to forget the worktrees of gate copies whose folder is gone. A gate copy is a worktree whose path lies under root,
// the folder that obee owns for them. Each record goes by its path, so the record of a worktree that the owner made, and whose folder is away (an
// unmounted disk), stays. It does nothing for a folder that is not a repository.
func forgetCopies(ctx context.Context, dir, root string) {
	ctx, cancel := context.WithTimeout(ctx, forgetLimit)
	defer cancel()

	out, err := gitIn(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return
	}

	// Git prints the real path of a worktree, and root may reach it through a link.
	resolved, _ := filepath.EvalSymlinks(root) //nolint:errcheck // a root that cannot be resolved matches by its own name only
	roots := []string{root, resolved}

	for line := range strings.Lines(out) {
		path, found := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "worktree ")
		if !found || !underAny(roots, path) {
			continue
		}

		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			_, _ = gitIn(ctx, dir, "worktree", "remove", "--force", path) //nolint:errcheck // a record that stays is tried again by the next gate
		}
	}
}

// underAny says whether path lies inside one of the folders.
func underAny(roots []string, path string) bool {
	for _, root := range roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return true
		}
	}

	return false
}

// forgetLimit bounds the git calls that forget the worktrees of gates that ended.
const forgetLimit = 30 * time.Second

// disposableTree returns the folder that the gate command works in. When dir is a git work tree, that is a detached worktree of its head, made under tmp,
// and the function that removes the record of the worktree from the repository. The command then writes a copy that obee throws away: the real git
// folder (its hooks, its config and the folder that core.hooksPath names) and the real work folder (.husky, .claude) are outside the plan, so a gate
// cannot plant a hook or an agent setting that runs later as the owner. A folder with no .git holds no hooks and is the work folder itself.
func disposableTree(ctx context.Context, dir, tmp string) (string, func(), error) {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return dir, func() {}, nil
	}

	work := filepath.Join(tmp, "work")

	// Records of copies that an earlier gate left, because it was killed, go before this gate adds one.
	forgetCopies(ctx, dir, filepath.Dir(tmp))

	if out, err := gitIn(ctx, dir, "worktree", "add", "--detach", "--quiet", work, "HEAD"); err != nil {
		return "", nil, fmt.Errorf("make the worktree for the gate: %w: %s", err, out)
	}

	// The record of the worktree is forgotten after the removal of the copy, so a rev-parse that fails leaks nothing.
	record, err := gitIn(ctx, work, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", nil, fmt.Errorf("find the git record of the worktree for the gate: %w: %s", err, record)
	}

	return work, func() { _ = os.RemoveAll(strings.TrimSpace(record)) }, nil
}

// gitIn runs git in dir and returns what it printed.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with arguments that obee writes
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// runIn makes the disposable work folder of dir under the temporary folder tmp, makes the plan for it and runs the command there.
func (g gateSandbox) runIn(ctx context.Context, enforcer sandbox.Enforcer, project config.Project, tmp, dir string, argv, env []string, limit time.Duration) (string, int) {
	work, release, err := disposableTree(ctx, dir, tmp)
	if err != nil {
		return notStarted(err.Error())
	}

	defer release()

	plan, note := g.plan(ctx, project, dir, work, tmp)
	if note != "" {
		return notStarted(note)
	}

	command := sandbox.Command{Argv: argv, Dir: work, Env: append(slices.Clone(g.envOf(env)), "TMPDIR="+tmp)}

	return execute(ctx, enforcer, plan, command, limit)
}

// envOf returns env, or the environment that the sandbox was made with when env is nil.
func (g gateSandbox) envOf(env []string) []string {
	if env == nil {
		return g.environ
	}

	return env
}

// notStarted is the output and the code of a gate that the sandbox could not start.
func notStarted(reason string) (string, int) { return "obee sandbox: " + reason + "\n", sandboxFailed }

// withLimit returns ctx with a deadline of limit, or ctx itself when limit is not set.
func withLimit(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if limit > 0 {
		return context.WithTimeout(ctx, limit)
	}

	return ctx, func() {}
}

// execute runs the command under the enforcer, with the limit when it is set, and puts the plan before the output.
func execute(ctx context.Context, enforcer sandbox.Enforcer, plan sandbox.Plan, command sandbox.Command, limit time.Duration) (string, int) {
	ctx, cancel := withLimit(ctx, limit)
	defer cancel()

	var out bytes.Buffer

	command.Stdout, command.Stderr = &out, &out

	code, err := sandbox.Execute(ctx, enforcer, plan, command)
	text := plan.Describe(enforcer.Name()) + "\n" + out.String()

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return text, gate.ExitTimedOut
	case err != nil:
		return text + "obee sandbox: " + err.Error() + "\n", sandboxFailed
	}

	return text, code
}

// plan makes the plan of a gate command. The command may read the system folders, the work folder work (the disposable copy), the shared git directories
// of the real work tree dir and the sandbox_read paths of the project, and nothing else under the home folder. It may write the whole work folder, its own
// temporary folder and the gate_allow_write paths. It may reach the gate_network domains through the proxy and nothing else. The real work folder and the real
// git folder are never writable: a gate_allow_write path that is or holds one of them, or the hooks folder that core.hooksPath names, is refused.
func (g gateSandbox) plan(ctx context.Context, project config.Project, dir, work, tmp string) (sandbox.Plan, string) {
	home, err := g.home()
	if err != nil {
		return sandbox.Plan{}, "obee cannot find the home folder, so the sandbox cannot deny it. The gate did not run."
	}

	deny := credentialPaths(home, "", project.SandboxDeny)
	read := expandAll(project.SandboxRead, home)

	if note := readNote(read, home, deny); note != "" {
		return sandbox.Plan{}, note
	}

	protected := slices.Concat(deny, closedPaths(ctx, dir, home), sharedGitDirs(dir))

	write, note := writablePaths(project.GateAllowWrite, home, work, protected)
	if note != "" {
		return sandbox.Plan{}, strings.Replace(note, " The step did not start.", "", 1) + " The gate did not run."
	}

	return planOf(project.GateNetwork, work, tmp, read, write), ""
}

// planOf joins the system paths, the work folder, the temporary folder and the lists of the project into one plan.
func planOf(network []string, work, tmp string, read, write []string) sandbox.Plan {
	return sandbox.Plan{
		Read:    slices.Concat(sandbox.SystemRead(), []string{work}, sharedGitDirs(work), read),
		Write:   slices.Concat(sandbox.SystemWrite(), []string{work, tmp}, write),
		Network: len(network) > 0,
		Domains: slices.Clone(network),
	}
}
