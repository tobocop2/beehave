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

// swarmLock orders the reads and writes of the pull request file: the engine calls the forge from several runs at once.
var swarmLock sync.Mutex

// e2eSwarm is the beehaveswarm command of the scripts: the real composition root, with the stand-in forge in place of the GitHub adapter.
func e2eSwarm() {
	syscall.Umask(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()
	idle := func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) }
	factory := flowFactoryFrom(func(ctx context.Context, cfg config.Config, opts flow.Options) (*flow.Engine, error) {
		table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), nil)
		swarmForge := table.forges[model.ForgeGithub]
		swarmForge.build = func(model.Project) (forge.Forge, error) {
			return swarmForgeAt{remote: env[swarmRemote], dir: env[swarmFolder]}, nil
		}
		table.forges[model.ForgeGithub] = swarmForge

		return newEngineOn(ctx, cfg, opts, idle, table)
	})
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

// branches lists the branches of the pull requests in the order of their numbers, and adds each branch that the remote has and the file lacks.
func (f swarmForgeAt) branches(ctx context.Context) ([]string, error) {
	swarmLock.Lock()
	defer swarmLock.Unlock()

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
		if name != "main" && !slices.Contains(known, name) {
			known = append(known, name)
		}
	}

	return known, os.WriteFile(f.path("prs"), []byte(strings.Join(known, "\n")+"\n"), e2eFileMode) //nolint:gosec // a test writes into its own temporary folder
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

	names, err := f.git(ctx, "diff", "--name-only", "refs/heads/main...refs/heads/"+branch)
	if err != nil {
		return model.PullRequest{}, err
	}

	var files []any
	for _, name := range strings.Fields(names) {
		files = append(files, map[string]any{"path": name})
	}

	row := map[string]any{
		"number": pr, "state": "OPEN", "isDraft": false, "baseRefName": "main", "headRefName": branch,
		"headRefOid": head, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": swarmAgentLogin},
		"files":   files,
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

func (f swarmForgeAt) PRView(ctx context.Context, pr int, _ ...string) (model.PullRequest, error) {
	branches, err := f.branches(ctx)
	if err != nil {
		return model.PullRequest{}, err
	}

	return f.view(ctx, branches, pr)
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

func (swarmForgeAt) StackID(context.Context, int) (string, error) { return "", nil }

func (swarmForgeAt) TokenScopes(context.Context) ([]string, error) { return []string{"repo"}, nil }

func (swarmForgeAt) TreeBlobs(context.Context, string) (map[string]string, error) { return nil, nil }

func (swarmForgeAt) DefaultBranch(context.Context) (string, error) { return "main", nil }

func (swarmForgeAt) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

func (f swarmForgeAt) IssueState(context.Context, int) (string, error) { return "", errFake }

func (f swarmForgeAt) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{}, errFake
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

	base, err := f.git(ctx, "rev-parse", "refs/heads/main")
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

	if _, err := f.git(ctx, "update-ref", "refs/heads/main", commit); err != nil {
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

	return f.pauseAfterMerge()
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
// until the spend ledger holds the cost of the task that the file names. The cost is the last step of a task run, so the run is over when the landing goes on,
// and a script can end a run in the middle of a landing on every run, and not only under load.
// The wait has a deadline (30 seconds): a barrier that nothing opens is a fault of the script, and the merge fails with that text.
func (f swarmForgeAt) pauseAfterMerge() error {
	named, err := os.ReadFile(f.path("pause-after-merge"))
	if err != nil {
		return nil //nolint:nilerr // no barrier file: the merge goes on at once
	}

	want := "\t" + strings.TrimSpace(string(named)) + "\ttask\t"

	for range 150 {
		if ledger, err := os.ReadFile(filepath.Join(f.dir, "..", "home", ".swarm", "swarm-1", "ledger.tsv")); err == nil && strings.Contains(string(ledger), want) {
			return nil
		}

		time.Sleep(200 * time.Millisecond)
	}

	return errBarrierClosed
}

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

	if err := work.WriteFile("fake/claude", []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	swarmInit(ts, ts.Getenv("WORK"), work)
}

// swarmInit makes the rest of a swarm script: the files of the example, two logins, the project, the bare remote with one commit on main
// (a Makefile whose check writes a JUnit report), the forge folder, and the variables of the run.
func swarmInit(ts *testscript.TestScript, dir string, work *os.Root) {
	if err := os.CopyFS(filepath.Join(dir, "swarm"), os.DirFS(filepath.Join("..", "..", "examples", "swarm"))); err != nil {
		ts.Fatalf("swarmfakes: %v", err)
	}

	remote := filepath.Join(dir, "remote.git")
	files := map[string]string{
		"config/runners.star":        "agents(max_concurrent = 2)\nrunner(name = \"one\", agent = \"claude\", config_dir = \"" + dir + "\")\nrunner(name = \"two\", agent = \"claude\", config_dir = \"" + dir + "\")\n",
		"config/projects/swarm.star": "project(repo = \"o/r\", source = \"" + remote + "\", forge = \"github\", base_branch = \"main\", gate = \"make check\", test_report = \"reports/*.xml\")\n",
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
		"REPO": "o/r", "BASE": "main", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SWARM": "swarm-1", "SWARM_DIR": filepath.Join(dir, "swarm"),
		"CAP_USD": "100", "REMOTE": remote, swarmRemote: remote, swarmFolder: filepath.Join(dir, "forge"), "HOME": filepath.Join(dir, "home"),
	} {
		ts.Setenv(name, value)
	}
}
