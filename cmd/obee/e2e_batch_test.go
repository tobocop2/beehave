package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// The batch scripts run batch_merge in a real obee process over a real git repository (the folder "tree" of the work folder) and a forge that lives in files.
// The pull request of the item mN is number N, on the branch mN. A merge on this forge makes a real merge commit on the branch main of the repository,
// writes the file forge/merged-N, and adds a line to forge/calls.log. When the file forge/crash-after holds a number, the process exits right after the merge with that count,
// once: the file forge/crashed stops the second exit. The exit comes before the engine records the merge step, as a power loss does.

const (
	e2eForgeFolder = "forge"
	e2eCrashAfter  = "crash-after"
	e2eCrashed     = "crashed"
	e2eCrashCode   = 9
	e2eMembers     = 3
)

// workTree is the worktree finder of the batch scripts: every item works in the folder "tree" of the work folder.
type workTree struct{ dir string }

func (t workTree) Path(context.Context, string, string) (string, error) {
	return filepath.Join(t.dir, "tree"), nil
}

// e2eBatch returns the merger, the batch host and the command runner of the batch scripts, or false when the script made no forge folder.
func e2eBatch(dir string, cfg config.Config) (pulls, batchHost, programs, bool) {
	if _, err := os.Stat(filepath.Join(dir, e2eForgeFolder)); err != nil {
		return pulls{}, batchHost{}, programs{}, false
	}

	p := pulls{projects: cfg.Projects, forge: func(model.Project) (forge.Forge, error) { return fileForge{dir: dir}, nil }}
	commands := programs{
		trees: workTree{dir: dir},
		local: func(binary, folder string, timeout time.Duration) proc.Runner {
			return proc.ExecRunner{Binary: binary, Dir: folder, Timeout: timeout}
		},
		exec: func(spec proc.ExecRunner) proc.Runner { return spec },
	}

	return p, batchHost{pulls: p, programs: commands}, commands, true
}

// withBatch adds the collaborators of the batch scripts to the dependencies.
func withBatch(deps flow.Deps, dir string, cfg config.Config) flow.Deps {
	if p, host, commands, ok := e2eBatch(dir, cfg); ok {
		deps.Merge, deps.Batch, deps.Commands = p, host, commands
	}

	return deps
}

// fileForge is the forge of the batch scripts. A method that it does not define panics, as it does in the merge tests.
type fileForge struct {
	forge.Forge

	dir string
}

func (f fileForge) tree() string { return filepath.Join(f.dir, "tree") }

// git runs git in the repository with a fixed identity and returns the first line of the output.
func (f fileForge) git(ctx context.Context, args ...string) (string, error) {
	out := proc.NewExecRunner("git", f.tree()).Run(ctx, append([]string{"-c", "user.name=forge", "-c", "user.email=forge@example.com"}, args...))
	if out.ExitCode != 0 {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(out.Stderr))
	}

	first, _, _ := strings.Cut(out.Stdout, "\n")

	return strings.TrimSpace(first), nil
}

func (f fileForge) path(name string) string { return filepath.Join(f.dir, e2eForgeFolder, name) }

// branchNumber reads N from the branch mN.
func branchNumber(branch string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(branch, "m"))
	if err != nil {
		return 0
	}

	return n
}

func (f fileForge) mergedAt(pr int) (string, bool) {
	text, err := os.ReadFile(f.path("merged-" + strconv.Itoa(pr)))

	return strings.TrimSpace(string(text)), err == nil
}

func (f fileForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	var head, state string

	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--head":
			head = args[i+1]
		case "--state":
			state = args[i+1]
		}
	}

	pr := branchNumber(head)
	if _, merged := f.mergedAt(pr); pr == 0 || merged != (state == "merged") {
		return nil, nil
	}

	return []model.PullRequest{{Number: pr}}, nil
}

func (f fileForge) PRView(ctx context.Context, pr int, _ ...string) (model.PullRequest, error) {
	head, err := f.git(ctx, "rev-parse", "refs/heads/m"+strconv.Itoa(pr))
	if err != nil {
		return model.PullRequest{}, err
	}

	row := map[string]any{
		"number": pr, "state": "OPEN", "isDraft": false, "baseRefName": "main", "headRefName": "m" + strconv.Itoa(pr),
		"headRefOid": head, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": "dev"},
		"files":   []any{map[string]any{"path": "f" + strconv.Itoa(pr) + ".txt"}},
		"commits": []any{map[string]any{"oid": head, "messageHeadline": "add", "messageBody": "", "authors": []any{map[string]any{"email": "dev@x", "login": "dev"}}}},
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

func (fileForge) PullCounts(context.Context, int) (commits, files int, err error) { return 1, 1, nil }

func (fileForge) Committers(context.Context, int) ([]model.Committer, error) {
	return []model.Committer{{}}, nil
}

func (fileForge) PRChecks(context.Context, int) ([]model.Check, error) {
	return []model.Check{{Name: "ci", Bucket: model.CheckPass}}, nil
}

func (fileForge) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return nil, nil
}

func (fileForge) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return nil, nil
}

func (fileForge) StackID(context.Context, int) (string, error) { return "", nil }

func (fileForge) TokenScopes(context.Context) ([]string, error) { return []string{"repo"}, nil }

func (fileForge) TreeBlobs(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f fileForge) BranchRef(ctx context.Context, branch string) (string, bool, error) {
	tip, err := f.git(ctx, "rev-parse", "refs/heads/"+branch)

	return tip, err == nil, nil
}

func (fileForge) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

// IsAncestor answers as git does for the repository of the forge: exit 0 means the commit is in the history of the base, exit 1 means it is not.
func (f fileForge) IsAncestor(ctx context.Context, commit, base string) (bool, error) {
	out := proc.NewExecRunner("git", f.tree()).Run(ctx, []string{"merge-base", "--is-ancestor", commit, "refs/heads/" + base})
	switch out.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}

	return false, fmt.Errorf("git merge-base: %s", strings.TrimSpace(out.Stderr))
}

func (f fileForge) CommitTree(ctx context.Context, commit string) (string, error) {
	return f.git(ctx, "rev-parse", commit+"^{tree}")
}

// MergePR merges the head into main as a merge commit, records the merge, and exits the process after the merge that the crash file names.
func (f fileForge) MergePR(ctx context.Context, pr int, sha string) error {
	base, err := f.git(ctx, "rev-parse", "refs/heads/main")
	if err != nil {
		return err
	}

	tree, err := f.git(ctx, "merge-tree", "--write-tree", base, sha)
	if err != nil {
		return err
	}

	commit, err := f.git(ctx, "commit-tree", tree, "-p", base, "-p", sha, "-m", "merge")
	if err != nil {
		return err
	}

	if _, err := f.git(ctx, "update-ref", "refs/heads/main", commit); err != nil {
		return err
	}

	if err := os.WriteFile(f.path("merged-"+strconv.Itoa(pr)), []byte(time.Now().UTC().Format(time.RFC3339)), e2eFileMode); err != nil {
		return err
	}

	log, err := os.OpenFile(f.path("calls.log"), os.O_APPEND|os.O_CREATE|os.O_RDWR, e2eFileMode)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(log, "MergePR %d %s\n", pr, sha); err != nil {
		return errJoin(err, log.Close())
	}

	if err := log.Close(); err != nil {
		return err
	}

	f.crashAfter()

	return nil
}

// crashAfter exits the process when the number of merges is the number in the crash file, once.
func (f fileForge) crashAfter() {
	want, err := os.ReadFile(f.path(e2eCrashAfter))
	if err != nil {
		return
	}

	calls, err := os.ReadFile(f.path("calls.log"))
	if err != nil || strconv.Itoa(strings.Count(string(calls), "\n")) != strings.TrimSpace(string(want)) {
		return
	}

	if _, err := os.Stat(f.path(e2eCrashed)); err == nil {
		return
	}

	if err := os.WriteFile(f.path(e2eCrashed), nil, e2eFileMode); err != nil {
		return
	}

	os.Exit(e2eCrashCode)
}

// batchStar is the workflow of the batch scripts. The heads are the commits of the branches m1 to m3 of the repository.
const batchStar = `def main(item):
    done = batch_merge(
        name = "all",
        base = "main",
        repo = "example/shop",
        own = {"logins": ["me"], "emails": ["me@x"]},
        gate_run = ["/bin/sh", "-c", "echo gate >> %s"],
        prs = [
%s        ],
    )
    if done.merged != ["m1", "m2", "m3"] or done.rejected or done.deferred:
        fail("the batch did not land all three: merged=%%s rejected=%%s deferred=%%s" %% (done.merged, done.rejected, done.deferred))
`

// cmdBatchSetup is "batchsetup": it makes the repository with the branches m1 to m3, the forge folder, and the workflow file flow/batch.star.
func cmdBatchSetup(ts *testscript.TestScript, _ bool, _ []string) {
	work := ts.Getenv(e2eDirVariable)
	run := func(args ...string) string {
		out := proc.NewExecRunner("git", filepath.Join(work, "tree")).Run(context.Background(), append([]string{"-c", "user.name=dev", "-c", "user.email=dev@x"}, args...))
		if out.ExitCode != 0 {
			ts.Fatalf("git %s: %s", args[0], out.Stderr)
		}

		return strings.TrimSpace(out.Stdout)
	}

	run("branch", "-M", "main")
	run("remote", "add", "origin", ".")

	var members strings.Builder

	for i := 1; i <= e2eMembers; i++ {
		name := "m" + strconv.Itoa(i)
		file := "f" + strconv.Itoa(i) + ".txt"

		run("checkout", "-q", "-b", name, "main")
		ts.Check(os.WriteFile(filepath.Join(work, "tree", file), []byte(name+"\n"), e2eFileMode))
		run("add", file)
		run("commit", "-q", "-m", "add "+file)
		fmt.Fprintf(&members, "            struct(item = %q, head = %q),\n", name, run("rev-parse", "HEAD"))
		run("checkout", "-q", "main")
	}

	ts.Check(os.MkdirAll(filepath.Join(work, e2eForgeFolder), e2eFolderMode))
	ts.Check(os.WriteFile(filepath.Join(work, "flow", "batch.star"), []byte(fmt.Sprintf(batchStar, filepath.Join(work, e2eForgeFolder, "gate.log"), members.String())), e2eFileMode))
}
