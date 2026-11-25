package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/fstree"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/sandbox"
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
	// clones is the lock of the clone of each project. The gate adds, lists and removes the worktree of its copy under it.
	clones cloneLock
	// folders is the set of the folders that the engine made. The engine builds one set, and a second set on one root removes the live folders of the first.
	folders *fstree.Folders
	// ownGit is true for a command call: git reads the identity of the engine in place of the config file of the owner, which the plan closes.
	ownGit bool
	// developer is the developer folder of Apple that the engine found at start, or "" when it found none. A gate plan reads it, and the command env names it.
	developer string
}

func newGateSandbox(projects map[string]config.Project, clones cloneLock, folders *fstree.Folders) gateSandbox {
	environ := model.EnvPairs(config.ProcessEnv())

	return gateSandbox{
		projects: projects, enforcer: chooseEnforcer, home: os.UserHomeDir, environ: environ, clones: clones, folders: folders, developer: developerFolder(environ),
	}
}

// asked returns the sandbox for a command call whose keyword is box: the network list of the call replaces gate_network of the project. A nil box
// is a gate, which keeps the lists of the project.
func (g gateSandbox) asked(project string, box *model.Sandbox) gateSandbox {
	if box == nil {
		return g
	}

	found := g.projects[project]
	found.GateNetwork = slices.Clone(box.Network)

	g.projects = maps.Clone(g.projects)
	g.projects[project] = found
	g.ownGit = true

	return g
}

// applies says whether a gate of the project runs under the sandbox on this machine: the project has the sandbox on and the gate runs here.
func (g gateSandbox) applies(project string, program bool) bool {
	return config.GateRefused(g.projects[project].Project, program)
}

// run runs argv in dir under the plan of the project and returns what it printed and its exit code. The first line of the output is the plan,
// so the report of the gate step records it. The gate command ran no code when the code is sandboxFailed and the first words are "beehave sandbox".
func (g gateSandbox) run(ctx context.Context, project, dir string, argv, env []string, limit time.Duration) (out string, code int) {
	return g.runAt(ctx, project, folder{path: dir}, argv, env, limit)
}

// runGate is run for the command of the project gate: the files that the command writes below the test_report glob of the project in the copy of the work
// folder are carried to the real folder, where the gate reads them.
func (g gateSandbox) runGate(ctx context.Context, project, dir string, argv, env []string, limit time.Duration) (out string, code int) {
	return g.runAt(ctx, project, folder{path: dir, report: g.projects[project].TestReport}, argv, env, limit)
}

// runJob is run for the job of a gate_program in the empty folder root that beehave made. The job clones the head from the repository at from, so the plan
// also lets it read that repository, its shared git folders and the source of the project when that is a folder.
func (g gateSandbox) runJob(ctx context.Context, project, from, root string, argv []string, limit time.Duration) (out string, code int) {
	g.ownGit = true

	return g.runAt(ctx, project, folder{path: root, own: true, readFrom: from}, argv, nil, limit)
}

// folder is the folder that a gate command works in. own is true for an empty folder that beehave made for the command: the command may write it. A folder
// that is not own and has no .git is the real work folder of a project with no repository. report is the test_report glob to carry out of the copy, or empty.
// readFrom is a repository that the command may also read, or empty.
type folder struct {
	path     string
	own      bool
	report   string
	readFrom string
}

// readable returns the paths that the command may read besides its folder: the repository readFrom with its shared git folders, and the local source of the project.
func (at folder) readable(project config.Project, git projectGit) []string {
	if at.readFrom == "" {
		return nil
	}

	paths := append([]string{at.readFrom}, sharedGitDirs(git.clones, at.readFrom)...)
	if filepath.IsAbs(project.Source) {
		paths = append(paths, project.Source)
	}

	return paths
}

// runOwn is run for a folder that beehave made for the command alone.
func (g gateSandbox) runOwn(ctx context.Context, project, dir string, argv, env []string, limit time.Duration) (out string, code int) {
	return g.runAt(ctx, project, folder{path: dir, own: true}, argv, env, limit)
}

// runAt is run for the folder at. A folder with no .git that is not own is read-only for the project gate and the program gate alike.
func (g gateSandbox) runAt(ctx context.Context, project string, at folder, argv, env []string, limit time.Duration) (out string, code int) {
	dir := at.path
	found := g.projects[project]

	enforcer, err := g.enforcer(found.SandboxEnforcer)
	if err != nil {
		return notStarted(err.Error())
	}

	tmp, err := g.gateTemp()
	if err != nil {
		return notStarted(fmt.Sprintf("make the temporary folder: %v", err))
	}

	clone := gateClone{hold: g.clones.of(project), git: gitOf(g.projects, project, gitexec.NewRepositories(g.clones.repos))}

	defer func() { out += discard(context.WithoutCancel(ctx), tmp, dir, clone) }()

	return g.runIn(ctx, enforcer, found, tmp, at, clone, argv, env, limit)
}

// gateClone is the clone whose worktree a gate copies: its lock, and the git of the project that runs each git call of the copy.
type gateClone struct {
	hold underLock
	git  projectGit
}

// underLock runs fn under the lock of the clone whose worktree a gate copies.
type underLock func(ctx context.Context, fn func() error) error

// of returns the lock of the clone of the project as an underLock.
func (l cloneLock) of(project string) underLock {
	return func(ctx context.Context, fn func() error) error {
		return l.hold(ctx, project, func(string) error { return fn() })
	}
}

// discard removes the copy of a gate, also when the context is done, and then tells the repository in dir to forget the records of gate copies whose folder
// is gone. That covers the record that the gate could not name. It returns a line for the output when the copy stays.
func discard(ctx context.Context, tmp, dir string, clone gateClone) string {
	note := ""
	if err := fstree.Remove(tmp); err != nil {
		note = fmt.Sprintf("beehave sandbox: the copy of the gate stays at %s: %v\n", tmp, err)
	}

	if isRepository(dir) {
		//nolint:errcheck // a lock that fails leaves the records, and the next gate tries again
		_ = clone.hold(ctx, func() error {
			forgetCopies(ctx, clone.git.Runner(dir, 0), filepath.Dir(tmp))

			return nil
		})
	}

	return note
}

// isRepository says whether dir holds a .git entry, so git commands in dir act on a repository.
func isRepository(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))

	return err == nil
}

// ownerOnly is the mode of the folders that beehave makes for gate copies.
const ownerOnly = 0o700

// gateTemp makes the temporary folder of one gate under the one folder that beehave owns for them.
func (g gateSandbox) gateTemp() (string, error) {
	return g.folders.Make(filepath.Join(os.TempDir(), fmt.Sprintf("beehave-gate-%d", os.Getuid())))
}

// forgetCopies tells the repository of git to forget the worktrees of gate copies whose folder is gone. A gate copy is a worktree whose path lies under root,
// the folder that beehave owns for them. Each record goes by its path, so the record of a worktree that the owner made, and whose folder is away (an
// unmounted disk), stays. It does nothing for a folder that is not a repository.
func forgetCopies(ctx context.Context, git gitexec.Runner, root string) {
	ctx, cancel := context.WithTimeout(ctx, forgetLimit)
	defer cancel()

	out, err := gitIn(ctx, git, "worktree", "list", "--porcelain")
	if err != nil {
		return
	}

	// Git prints the real path of a worktree, and root may reach it through a link.
	roots := []string{root, shell.RealPath(root)}

	for _, path := range worktreePaths(out) {
		if !underAny(roots, path) {
			continue
		}

		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			_, _ = gitIn(ctx, git, "worktree", "remove", "--force", path) //nolint:errcheck // a record that stays is tried again by the next gate
		}
	}
}

// underAny says whether path lies inside one of the folders, and is not the folder itself.
func underAny(roots []string, path string) bool {
	return slices.ContainsFunc(roots, func(root string) bool {
		return nameInside(root, path) && filepath.Clean(path) != filepath.Clean(root)
	})
}

// forgetLimit bounds the git calls that forget the worktrees of gates that ended.
const forgetLimit = 30 * time.Second

// disposableTree returns the folder that the gate command works in. When dir is a git work tree, that is a detached worktree of its head, made under tmp,
// and the function that removes the record of the worktree from the repository. The command then writes a copy that beehave throws away: the real git
// folder (its hooks, its config and the folder that core.hooksPath names) and the real work folder (.husky, .claude) are outside the plan, so a gate
// cannot plant a hook or an agent setting that runs later as the owner. A folder with no .git holds no hooks and is the work folder itself. The repository
// that it returns names the work folder in WorkTree, and the git folders of the copy when there is one.
func disposableTree(ctx context.Context, dir, tmp string, clone gateClone) (gitexec.Repository, func(), error) {
	if !isRepository(dir) {
		return gitexec.Repository{WorkTree: dir}, func() {}, nil
	}

	work := filepath.Join(tmp, "work")

	if err := clone.hold(ctx, func() error { return addCopy(ctx, clone, dir, tmp, work) }); err != nil {
		return gitexec.Repository{}, nil, err
	}

	// The record of the worktree is forgotten after the removal of the copy, so a rev-parse that fails leaks nothing.
	// The record is read before the gate command starts, so the command cannot move it.
	record, err := clone.git.copyRecord(ctx, work)
	if err != nil {
		return gitexec.Repository{}, nil, fmt.Errorf("find the git record of the worktree for the gate: %w", err)
	}

	record.WorkTree = work

	return record, func() {
		//nolint:errcheck // a record that stays is forgotten by the discard of the copy, or by the next gate
		_ = clone.hold(context.WithoutCancel(ctx), func() error { return os.RemoveAll(record.GitDir) })
	}, nil
}

// addCopy adds the worktree work for a gate in the repository of dir. Records of copies that an earlier gate left, because it was killed, go first.
func addCopy(ctx context.Context, clone gateClone, dir, tmp, work string) error {
	forgetCopies(ctx, clone.git.Runner(dir, 0), filepath.Dir(tmp))

	if out, err := gitIn(ctx, clone.git.Runner(dir, 0), "worktree", "add", "--detach", "--quiet", work, "HEAD"); err != nil {
		return fmt.Errorf("make the worktree for the gate: %w: %s", err, out)
	}

	return nil
}

// gitIn runs git with the runner git and returns what it printed. A canceled context kills the process group of git, and a child that holds the output open
// does not keep the call past the grace period of the runner.
func gitIn(ctx context.Context, git gitexec.Runner, args ...string) (string, error) {
	result := git.Run(ctx, args)
	out := result.Stdout + result.Stderr

	if result.ExitCode != 0 {
		return out, fmt.Errorf("git %s: %s", gitFailure(ctx, result), strings.TrimSpace(result.Stderr))
	}

	return out, nil
}

// gitFailure names why a git call failed: it did not start, the time ran out, the context was canceled, or it exited with a code.
func gitFailure(ctx context.Context, result model.CommandResult) string {
	switch {
	case result.SetupErr != nil:
		return fmt.Sprintf("could not start (%v)", result.SetupErr)
	case result.TimedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timed out"
	case errors.Is(ctx.Err(), context.Canceled):
		return "was canceled"
	default:
		return fmt.Sprintf("exited with code %d", result.ExitCode)
	}
}

// runIn makes the disposable work folder of dir under the temporary folder tmp, makes the plan for it and runs the command there.
func (g gateSandbox) runIn(ctx context.Context, enforcer sandbox.Enforcer, project config.Project, tmp string, at folder, clone gateClone, argv, env []string,
	limit time.Duration,
) (string, int) {
	dir := at.path

	copied, release, err := disposableTree(ctx, dir, tmp, clone)
	if err != nil {
		return notStarted(err.Error())
	}

	defer release()

	return g.runCopied(ctx, enforcer, project, tmp, at, copied, clone, argv, env, limit)
}

// runCopied makes the plan for the work folder copied, runs the command there, and carries the test report back to the real folder of at.
func (g gateSandbox) runCopied(ctx context.Context, enforcer sandbox.Enforcer, project config.Project, tmp string, at folder, copied gitexec.Repository, clone gateClone,
	argv, env []string, limit time.Duration,
) (string, int) {
	dir := at.path

	// A folder with no .git has no copy. The plan then never grants it for writing, unless beehave made it for the command: a write there would change the
	// real folder, where a planted hook folder or agent setting runs later as the owner. A project with no repository still writes through gate_allow_write.
	plan, note := g.plan(ctx, project, cmp.Or(at.readFrom, dir), copied, tmp, clone.git, copied.WorkTree == dir && !at.own)
	if note != "" {
		return notStarted(note)
	}

	plan.Read = append(plan.Read, at.readable(project, clone.git)...)

	reports, err := startCarry(at.report, copied.WorkTree, dir)
	if err != nil {
		return notStarted(err.Error() + ". The gate did not run.")
	}

	command := sandbox.Command{Argv: argv, Dir: copied.WorkTree, Env: g.commandEnv(ctx, env, tmp, clone.git.Runner(dir, proc.DefaultTimeout))}

	// The gate reads the test report in the real work folder, and the command wrote it in the copy that beehave throws away.
	return reports.settle(execute(ctx, enforcer, plan, command, limit))
}

// commandEnv returns the environment of the command: env without the SSH agent, its temporary folder, and for a command call the git variables of the engine.
func (g gateSandbox) commandEnv(ctx context.Context, env []string, tmp string, git gitexec.Runner) []string {
	run := append(withoutSocket(g.envOf(env)), "TMPDIR="+tmp)
	if g.ownGit {
		run = append(run, gitVariables(gitIdentity(ctx, git))...)
	}

	return append(run, developerEnv(g.developer, tmp)...)
}

// gitVariables returns the variables that give git the identity of the engine and no config file of the owner: the plan closes ~/.gitconfig, so git would fail
// on it. The identity goes by GIT_CONFIG_COUNT, which git reads as config values, so no file holds it and no quoting is needed. A value that is empty is left out.
func gitVariables(identity agent.GitIdentity) []string {
	vars := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}

	var pairs [][2]string

	for _, pair := range [][2]string{{"user.name", identity.Name}, {"user.email", identity.Email}} {
		if pair[1] != "" {
			pairs = append(pairs, pair)
		}
	}

	vars = append(vars, "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))

	for i, pair := range pairs {
		vars = append(vars, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, pair[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, pair[1]))
	}

	return vars
}

// envOf returns env, or the environment that the sandbox was made with when env is nil. The caller drops the SSH agent from it.
func (g gateSandbox) envOf(env []string) []string {
	if env == nil {
		return g.environ
	}

	return env
}

// notStarted is the output and the code of a gate that the sandbox could not start.
func notStarted(reason string) (string, int) {
	return "beehave sandbox: " + reason + "\n", sandboxFailed
}

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
		return text + "beehave sandbox: " + err.Error() + "\n", sandboxFailed
	}

	return text, code
}

// plan makes the plan of a gate command. The command may read the system folders, the work folder of copied (the disposable copy), the common git folder
// of the copy and the sandbox_read paths of the project, and nothing else under the home folder. It may write the whole work folder, its own
// temporary folder and the gate_allow_write paths. A real folder with no .git has no copy, so it is read-only unless beehave made it for the command. It may reach the gate_network domains through the proxy and nothing else. The real work folder and the real
// git folder are never writable: a gate_allow_write path that is or holds one of them, or the hooks folder that core.hooksPath names, is refused.
func (g gateSandbox) plan(ctx context.Context, project config.Project, dir string, copied gitexec.Repository, tmp string, git projectGit, readOnly bool) (sandbox.Plan, string) {
	home, err := g.home()
	if err != nil {
		return sandbox.Plan{}, "beehave cannot find the home folder, so the sandbox cannot deny it. The gate did not run."
	}

	deny := withSocket(credentialPaths(home, "", project.SandboxDeny), socketOf(g.environ))
	read := expandAll(project.SandboxRead, home)

	if note := readNote(read, home, deny); note != "" {
		return sandbox.Plan{}, note
	}

	closed, refused := protected(ctx, git, dir, home, deny)
	write, note := writablePaths(project.GateAllowWrite, home, copied.WorkTree, closed)

	if note = cmp.Or(refused, note); note != "" {
		return sandbox.Plan{}, strings.Replace(note, " The step did not start.", "", 1) + " The gate did not run."
	}

	return planOf(project.GateNetwork, copied.WorkTree, tmp, slices.Concat(commonOf(copied), read, developerReads(g.developer)), write, readOnly, g.developer), ""
}

// protected returns the paths that no gate_allow_write path may reach: the credential paths deny, the closed paths of dir and its shared git folders.
// It returns the reason instead when the clone of dir is refused, so no plan leaves out the hooks folder of the clone.
func protected(ctx context.Context, git projectGit, dir, home string, deny []string) ([]string, string) {
	closed, err := closedPaths(ctx, git.Runner(dir, proc.DefaultTimeout), dir, home)
	if err != nil {
		return nil, err.Error()
	}

	return slices.Concat(deny, closed, sharedGitDirs(git.clones, dir)), ""
}

// planOf joins the system paths, the work folder, the temporary folder and the lists of the project into one plan. The work folder is read-only when readOnly is true.
func planOf(network []string, work, tmp string, read, write []string, readOnly bool, developer string) sandbox.Plan {
	own := []string{work, tmp}
	if readOnly {
		own = []string{tmp}
	}

	return sandbox.Plan{
		Read:    slices.Concat(sandbox.SystemRead(), []string{work}, read),
		Write:   slices.Concat(sandbox.SystemWrite(), own, write),
		Network: len(network) > 0,
		Domains: slices.Clone(network),
		// The read list holds the developer folder, and the enforcer lets the shims of Apple run when this is set.
		Developer: developer,
	}
}
