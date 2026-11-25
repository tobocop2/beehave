package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// The swarm scripts run examples/swarm in the real engine, with the real local tracker, the real batch host and the real worktrees.
// Only the forge is a stand-in: the bare repository of the variable SWARM_REMOTE is both the remote of the agents and the forge. A pull request is
// a branch of that repository. Its number is its line in the file SWARM_FORGE/prs, in the order in which the forge first saw the branch.
// A merge on this forge makes a merge commit on the base in the bare repository, writes SWARM_FORGE/merged-N, and adds a line to SWARM_FORGE/calls.log.

const (
	swarmRemote = "SWARM_REMOTE"
	swarmFolder = "SWARM_FORGE"

	// The agents commit as their own account, not as the owner, so the merge guard does not treat their work as the work of the owner.
	swarmAgentLogin = "agent"
	swarmAgentEmail = "agent@example.com"
	swarmClaude     = "swarm-claude.sh"
)

// swarmPoll is the interval at which the engine of a swarm script looks for work and for the result of a run. It is short, so that a script does not wait for the engine default (seconds) at every step.
const swarmPoll = 50 * time.Millisecond

// swarmLock orders the reads and writes of the pull request file: the engine calls the forge from several runs at once.
var swarmLock sync.Mutex

// e2eSwarm is the beehaveswarm command of the scripts: the real composition root, with the stand-in forge in place of the GitHub adapter.
func e2eSwarm() {
	e2eStandIn(func(cfg config.Config) config.Config { return cfg }, func(_ config.Config, deps flow.Deps) flow.Deps { return deps })
}

// e2eStandIn runs the swarm composition. The engine builds its dependencies over the config that build gives, and adjust then changes them. It gets the config as the files wrote it.
func e2eStandIn(build func(config.Config) config.Config, adjust func(config.Config, flow.Deps) flow.Deps) {
	syscall.Umask(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()
	idle := func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) }
	factory := flowFactoryFrom(func(ctx context.Context, cfg config.Config, opts flow.Options) (*flow.Engine, error) {
		table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), nil)
		swarmForge := table.forges[forgegithub.Name]
		swarmForge.build = func(model.Project) (forge.Forge, error) {
			return swarmForgeAt{remote: env[swarmRemote], dir: env[swarmFolder]}, nil
		}
		table.forges[forgegithub.Name] = swarmForge

		deps := depsOn(build(cfg), opts, loginsReading(cfg, idle), table)
		deps.Clock.Poll = swarmPoll

		return flow.NewEngine(ctx, withModules(opts), adjust(cfg, deps))
	})
	e2eRecordRun(os.Args[1:])

	code := cli.Execute(ctx, os.Args[1:], buildInfo(env, e2eVersion, factory), env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// swarmForgeAt is the forge of the swarm scripts. A method that it does not define panics, as it does in the batch scripts.
type swarmForgeAt struct {
	forge.Forge

	remote, dir string
}

func (f swarmForgeAt) git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--git-dir", f.remote, "-c", "user.name=forge", "-c", "user.email=forge@example.com"}, args...)

	out := proc.NewExecRunner("git", "").Run(ctx, full)
	if out.ExitCode != 0 {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(out.Stderr))
	}

	return strings.TrimSpace(out.Stdout), nil
}

func (f swarmForgeAt) path(name string) string { return filepath.Join(f.dir, name) }

// base is the branch that every pull request targets: the first line of the file base in the forge folder, or main when the script made none.
func (f swarmForgeAt) base() string {
	text, err := os.ReadFile(f.path("base"))
	if err != nil || strings.TrimSpace(string(text)) == "" {
		return "main"
	}

	return strings.TrimSpace(string(text))
}

// opensOnCreate is true when the script made the file open-on-create in the forge folder. A pushed branch is then no pull request until a CreatePR call names it.
func (f swarmForgeAt) opensOnCreate() bool {
	_, err := os.Stat(f.path("open-on-create"))

	return err == nil
}

// branches lists the branches of the pull requests in the order of their numbers, and adds each branch that the remote has and the file lacks.
// With the file open-on-create it adds none: only CreatePR makes a pull request.
func (f swarmForgeAt) branches(ctx context.Context) ([]string, error) {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	return f.branchesLocked(ctx)
}

// branchesLocked is branches for a caller that holds swarmLock.
func (f swarmForgeAt) branchesLocked(ctx context.Context) ([]string, error) {
	text, err := os.ReadFile(f.path("prs"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	known := strings.Fields(string(text))

	listed, err := f.git(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}

	for _, name := range strings.Fields(listed) {
		if name != "main" && !slices.Contains(known, name) && !f.opensOnCreate() {
			known = append(known, name)
		}
	}

	return known, f.writeBranches(known)
}

func (f swarmForgeAt) writeBranches(known []string) error {
	return os.WriteFile(f.path("prs"), []byte(strings.Join(known, "\n")+"\n"), e2eFileMode) //nolint:gosec // a test writes into its own temporary folder
}

func (f swarmForgeAt) mergedAt(pr int) (string, bool) {
	text, err := os.ReadFile(f.path("merged-" + strconv.Itoa(pr)))

	return strings.TrimSpace(string(text)), err == nil
}

func (f swarmForgeAt) view(ctx context.Context, branches []string, pr int) (model.PullRequest, error) {
	if pr < 1 || pr > len(branches) {
		return model.PullRequest{}, fmt.Errorf("no pull request %d", pr)
	}

	branch := branches[pr-1]

	head, err := f.git(ctx, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return model.PullRequest{}, err
	}

	names, err := f.git(ctx, "diff", "--name-only", "refs/heads/"+f.base()+"...refs/heads/"+branch)
	if err != nil {
		return model.PullRequest{}, err
	}

	var files []any
	for _, name := range strings.Fields(names) {
		files = append(files, map[string]any{"path": name})
	}

	row := map[string]any{
		"number": pr, "state": "OPEN", "isDraft": false, "baseRefName": f.base(), "headRefName": branch,
		"headRefOid": head, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": swarmAgentLogin},
		"files": files, "labels": []any{}, "assignees": []any{},
		"commits": []any{map[string]any{"oid": head, "messageHeadline": "task", "messageBody": "", "authors": []any{map[string]any{"email": swarmAgentEmail, "login": swarmAgentLogin}}}},
	}

	if at, merged := f.mergedAt(pr); merged {
		row["state"], row["mergedAt"] = "MERGED", at
	}

	text, err := json.Marshal(row)
	if err != nil {
		return model.PullRequest{}, err
	}

	return model.Decode[model.PullRequest]("fake", string(text))
}

func (f swarmForgeAt) PRList(ctx context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	var head, state string

	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--head":
			head = args[i+1]
		case "--state":
			state = args[i+1]
		}
	}

	branches, err := f.branches(ctx)
	if err != nil {
		return nil, err
	}

	var found []model.PullRequest

	for number, branch := range branches {
		if _, merged := f.mergedAt(number + 1); (head != "" && branch != head) || merged != (state == "merged") {
			continue
		}

		view, err := f.view(ctx, branches, number+1)
		if err != nil {
			return nil, err
		}

		found = append(found, view)
	}

	return found, nil
}

// OpenPulls lists the open pull requests as the real forge does: the pull requests of PRList for the state open, and an error for each pull request that lacks a field.
func (f swarmForgeAt) OpenPulls(ctx context.Context, base string) ([]model.OpenPull, error) {
	rows, err := f.PRList(ctx, forge.OpenPullLimit, "--state", "open")
	if err != nil {
		return nil, err
	}

	var (
		pulls []model.OpenPull
		holes []error
	)

	for _, row := range rows {
		if open := row.Open(); base == "" || open.Base == base {
			pulls = append(pulls, open)
			holes = append(holes, open.Check())
		}
	}

	return pulls, errors.Join(holes...)
}

func (f swarmForgeAt) PRView(ctx context.Context, pr int, _ ...string) (model.PullRequest, error) {
	branches, err := f.branches(ctx)
	if err != nil {
		return model.PullRequest{}, err
	}

	return f.view(ctx, branches, pr)
}

func (f swarmForgeAt) PullRead(ctx context.Context, pr int) (model.PullFacts, error) {
	view, err := f.PRView(ctx, pr)

	return pullFactsOf(pr, view, err)
}

func (swarmForgeAt) PullCounts(context.Context, int) (commits, files int, err error) {
	return 1, 1, nil
}

func (swarmForgeAt) Committers(context.Context, int) ([]model.Committer, error) {
	return []model.Committer{{Login: swarmAgentLogin, Email: swarmAgentEmail}}, nil
}

func (swarmForgeAt) PRChecks(context.Context, int) ([]model.Check, error) {
	return []model.Check{{Name: "ci", Bucket: model.CheckPass}}, nil
}

func (swarmForgeAt) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return nil, nil
}

func (swarmForgeAt) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return nil, nil
}

// CreatePR finds the number of the pushed branch of the pull request, and with the file open-on-create it first makes the branch a pull request.
// It records the base that the call asked for in the file opened-<number> of the forge folder, so a script can check it. The pull request itself keeps the base of the file base.
func (f swarmForgeAt) CreatePR(ctx context.Context, req model.NewPull) (int, error) {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	branches, err := f.branchesLocked(ctx)
	if err != nil {
		return 0, err
	}

	number := slices.Index(branches, req.Head)
	if number < 0 && f.opensOnCreate() {
		if _, err = f.git(ctx, "rev-parse", "--verify", "refs/heads/"+req.Head); err != nil {
			return 0, err
		}

		branches = append(branches, req.Head)
		number = len(branches) - 1

		if err = f.writeBranches(branches); err != nil {
			return 0, err
		}
	}

	if number < 0 {
		return 0, fmt.Errorf("no branch %s", req.Head)
	}

	return number + 1, os.WriteFile(f.path("opened-"+strconv.Itoa(number+1)), []byte(req.Base+"\n"), e2eFileMode)
}

// Comments reads the comments of a pull request from the files <number>-<n> of the folder comments in the forge folder, in the order of n.
func (f swarmForgeAt) Comments(_ context.Context, pr int) ([]model.Comment, error) {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	names, err := filepath.Glob(f.path(filepath.Join("comments", strconv.Itoa(pr)+"-*")))
	if err != nil {
		return nil, err
	}

	rows := make([]model.Comment, 0, len(names))

	for _, name := range names {
		text, err := os.ReadFile(name) //nolint:gosec // G304: a test reads a file of the folder of its own script
		if err != nil {
			return nil, err
		}

		rows = append(rows, model.Comment{Body: string(text)})
	}

	return rows, nil
}

// PostComment writes the comment to the next file of the pull request in the folder comments.
func (f swarmForgeAt) PostComment(_ context.Context, pr int, body string) error {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	if err := os.MkdirAll(f.path("comments"), e2eFolderMode); err != nil {
		return err
	}

	names, err := filepath.Glob(f.path(filepath.Join("comments", strconv.Itoa(pr)+"-*")))
	if err != nil {
		return err
	}

	return os.WriteFile(f.path(filepath.Join("comments", strconv.Itoa(pr)+"-"+strconv.Itoa(len(names)+1))), []byte(body), e2eFileMode)
}

func (swarmForgeAt) LinkStack(context.Context, int, int, string) error { return nil }

func (swarmForgeAt) Unstack(context.Context, int) error { return nil }

func (swarmForgeAt) PRBody(context.Context, int) (string, error) { return "text", nil }

func (swarmForgeAt) SetBody(context.Context, int, string) error { return nil }

func (swarmForgeAt) StackID(context.Context, int) (string, error) { return "", nil }

func (swarmForgeAt) CanWriteCI(context.Context) (model.CIWrite, error) { return model.CIWriteNo, nil }

func (swarmForgeAt) TreeBlobs(context.Context, string) (map[string]string, error) { return nil, nil }

func (swarmForgeAt) DefaultBranch(context.Context) (string, error) { return "main", nil }

func (swarmForgeAt) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

func (f swarmForgeAt) IssueState(context.Context, int) (string, error) { return "", errFake }

// ClosingRefs reports that no pull request closes an issue, and that the base is the base of the forge.
func (f swarmForgeAt) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{BaseBranch: f.base(), DefaultBranch: "main"}, nil
}

func (f swarmForgeAt) BranchRef(ctx context.Context, branch string) (string, bool, error) {
	tip, err := f.git(ctx, "rev-parse", "refs/heads/"+branch)

	// If the file force-pushed exists in the forge folder, each branch except main reads as pushed again after the fold.
	if _, statErr := os.Stat(f.path("force-pushed")); statErr == nil && branch != "main" {
		return strings.Repeat("f", len(tip)), true, nil
	}

	return tip, err == nil, nil
}

// Compare answers as git does: exit 0 means the commit is in the history of the base, exit 1 means it is not.
func (f swarmForgeAt) Compare(ctx context.Context, commit, base string) (model.Comparison, error) {
	out := proc.NewExecRunner("git", "").Run(ctx, []string{"--git-dir", f.remote, "merge-base", "--is-ancestor", commit, "refs/heads/" + base})
	switch out.ExitCode {
	case 0:
		return model.Comparison{Status: "ahead", AheadBy: 1}, nil
	case 1:
		return model.Comparison{Status: "diverged", AheadBy: 1, BehindBy: 1}, nil
	}

	return model.Comparison{}, fmt.Errorf("git merge-base: %s", strings.TrimSpace(out.Stderr))
}

func (f swarmForgeAt) CommitTree(ctx context.Context, commit string) (string, error) {
	return f.git(ctx, "rev-parse", commit+"^{tree}")
}

// MergePR merges the head into main as a merge commit in the bare repository, and records the merge.
func (f swarmForgeAt) MergePR(ctx context.Context, pr int, sha string) error {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	if f.failsMerge(pr) {
		return errFake
	}

	base, err := f.git(ctx, "rev-parse", "refs/heads/"+f.base())
	if err != nil {
		return err
	}

	tree, err := f.git(ctx, "merge-tree", "--write-tree", base, sha)
	if err != nil {
		return err
	}

	tree, _, _ = strings.Cut(tree, "\n")

	commit, err := f.git(ctx, "commit-tree", tree, "-p", base, "-p", sha, "-m", "merge")
	if err != nil {
		return err
	}

	if _, err := f.git(ctx, "update-ref", "refs/heads/"+f.base(), commit); err != nil {
		return err
	}

	if err := os.WriteFile(f.path("merged-"+strconv.Itoa(pr)), []byte("2026-01-01T00:00:00Z"), e2eFileMode); err != nil {
		return err
	}

	log, err := os.OpenFile(f.path("calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, e2eFileMode)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(log, "MergePR %d %s\n", pr, sha)
	if err = errJoin(err, log.Close()); err != nil {
		return err
	}

	return f.pauseAfterMerge(ctx, swarmBarrierBound)
}

// failsMerge is the stand-in of a landing that the forge defers. If the file fail-merge exists in the forge folder, a merge fails while a task runs,
// and adds the line "MergeFail <pr>" to calls.log. The cause goes away by itself when the tasks end, as a transient cause does (checks that still run).
func (f swarmForgeAt) failsMerge(pr int) bool {
	if _, err := os.Stat(f.path("fail-merge")); err != nil {
		return false
	}

	// A task of the fake claude holds a marker in the folder running while it runs.
	running, err := os.ReadDir(filepath.Join(f.dir, "..", "running"))
	if err != nil || len(running) == 0 {
		return false
	}

	log, err := os.OpenFile(f.path("calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, e2eFileMode)
	if err != nil {
		return false
	}

	_, err = fmt.Fprintf(log, "MergeFail %d\n", pr)
	_ = log.Close()

	return err == nil
}

// pauseAfterMerge is the barrier of a landing. If the file pause-after-merge exists in the forge folder, the first merge waits, after it is recorded,
// until the stand-in agent has ended the task that the file names. The agent step is the last step of a task run, and the agent writes the file ended-<id> as it ends,
// so the run ends at the moment when the landing goes on, and a script can end a run in the middle of a landing on every run, and not only under load.
// It waits for that file. It ends when the file is there (nil), when the context of the merge ends (the error of ctx), or at bound (errBarrierClosed).
func (f swarmForgeAt) pauseAfterMerge(ctx context.Context, bound time.Duration) error {
	named, err := os.ReadFile(f.path("pause-after-merge"))
	if err != nil {
		return nil //nolint:nilerr // no barrier file: the merge goes on at once
	}

	id := strings.TrimSpace(string(named))
	opened := true

	waitOn(func(string, ...any) { opened = false }, e2eWaiting{what: "the end of the task " + id, ended: ctx.Done(), bound: bound, found: func() bool {
		_, err := os.Stat(filepath.Join(f.dir, "..", "ended-"+id)) //nolint:gosec // G703: a test reads a file of the folder of its own script

		return err == nil
	}})

	switch {
	case opened:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return errBarrierClosed
	}
}

const swarmBarrierBound = 30 * time.Second

var errBarrierClosed = errors.New("harness fault: the landing barrier was not opened within 30 seconds")

// cmdSwarmFakes is "swarmfakes": it writes the fake claude of the swarm scripts to fake/claude, with the work folder in place of the marker.
func cmdSwarmFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: swarmfakes")
	}

	text, err := os.ReadFile(filepath.Join("testdata", swarmClaude))
	if err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	if err := writeFake(ts, "fake/claude", text); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	sandboxHome(ts)
	swarmInit(ts, ts.Getenv("WORK"), work)
}

// swarmInit makes the rest of a swarm script: the files of the example, two logins, the project, the bare remote with one commit on main
// (a Makefile whose check writes a JUnit report), the forge folder, and the variables of the run.
func swarmInit(ts *testscript.TestScript, dir string, work *os.Root) {
	if err := os.CopyFS(filepath.Join(dir, "swarm"), os.DirFS(filepath.Join("..", "..", "examples", "swarm"))); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	remote := filepath.Join(dir, "remote.git")
	login := filepath.Join(dir, "login") // sandboxHome made it: the login folder is a credential path, so it is not the folder of the state
	// The project sets sandbox = "off" because isolate puts the tool wrappers, sh among them, in a test folder on PATH that a sandboxed gate may not read, so the merge gate fails.
	files := map[string]string{
		"config/runners.star":        "agents(max_concurrent = 2)\nrunner(name = \"one\", agent = \"claude\", config_dir = \"" + login + "\")\nrunner(name = \"two\", agent = \"claude\", config_dir = \"" + login + "\")\n",
		"config/projects/swarm.star": "project(repo = \"o/r\", source = \"" + remote + "\", forge = \"github\", base_branch = \"main\", gate = \"make check\", test_report = \"reports/*.xml\", sandbox = \"off\")\n",
		"seed/Makefile":              "check: ; @mkdir -p reports && echo '<testsuite tests=\"1\"><testcase name=\"t\"/></testsuite>' > reports/r.xml\n",
		"seed/.gitignore":            "reports/\n",
	}

	for name, text := range files {
		if err := work.MkdirAll(filepath.Dir(name), e2eFolderMode); err != nil {
			ts.Fatalf("swarmfakes: %v", err)
		}

		if err := work.WriteFile(name, []byte(text), e2eFileMode); err != nil {
			ts.Fatalf("swarmfakes: %v", err)
		}
	}

	seed := filepath.Join(dir, "seed")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "Makefile", ".gitignore"},
		{"-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "-q", "-m", "start"},
		{"clone", "-q", "--bare", ".", remote},
	} {
		if out := proc.NewExecRunner("git", seed).Run(context.Background(), args); out.ExitCode != 0 {
			ts.Fatalf("swarmfakes: git %s: %s", args[0], out.Stderr)
		}
	}

	if err := work.MkdirAll("forge", e2eFolderMode); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	for name, value := range map[string]string{
		"REPO": "o/r", "BASE": "main", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SWARM": "swarm-1",
		"REMOTE": remote, swarmRemote: remote, swarmFolder: filepath.Join(dir, "forge"), "HOME": filepath.Join(dir, "home"),
	} {
		ts.Setenv(name, value)
	}
}
