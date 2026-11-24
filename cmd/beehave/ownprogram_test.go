package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/ownprogram"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store"
)

// ownForge is the forge of the owner command: a fixed pull request, and the statuses that were posted.
type ownForge struct {
	forge.Forge

	pull   model.PullRequest
	posted *[]model.CommitStatus
}

func (f ownForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return f.pull, nil
}

func (ownForge) CloneURL() string { return "https://example.test/o/n.git" }

func (ownForge) Protection(context.Context, string) (model.Protection, error) {
	return model.Protection{}, nil
}

func (f ownForge) PostStatus(_ context.Context, status model.CommitStatus) error {
	*f.posted = append(*f.posted, status)

	return nil
}

// ownRepo is a source repository with a base commit on main and a head commit on the branch gate-change, and the head commit id.
type ownRepo struct {
	src  string
	head string
}

// newOwnRepo makes the source: the base has a failing program (scripts/check.sh exits 1) and code.txt. The head branch edits the program so that it passes,
// and also edits the files that headEdits names.
func newOwnRepo(t *testing.T, headEdits ...string) ownRepo {
	t.Helper()

	src := evaluated(t, t.TempDir())
	write := func(name, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(src, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(src, name), []byte(text), 0o600))
	}
	commit := func(message string) {
		runGit(t, src, "add", "-A")
		runGit(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", message)
	}

	runGit(t, src, "init", "-q", "-b", "main")
	write("scripts/check.sh", "exit 1\n")
	write("code.txt", "base\n")
	commit("base")
	runGit(t, src, "checkout", "-q", "-b", "gate-change")
	write("scripts/check.sh", "exit 0\n")

	for _, name := range headEdits {
		write(name, "head\n")
	}

	commit("head")

	return ownRepo{src: src, head: gitOut(t, src, "rev-parse", "HEAD")}
}

// ownRig builds the owner gate over a real clone of the source, with a fake forge. posted collects the statuses of the writer.
// The project has no credentials, because the clone of a project with credentials needs the roles that the engine loads. withWriter gives the returned
// gate a writer role after the clone is built, which only the post reads.
func ownRig(t *testing.T, repo ownRepo, withWriter bool) (ownGate, *[]model.CommitStatus) {
	t.Helper()

	return ownRigWith(t, repo, withWriter, ownTracker{})
}

// ownTracker is the tracker of the project: its open items, or the error of the read.
type ownTracker struct {
	items []model.Item
	err   error
}

func (ownTracker) ItemFields() []string { return []string{"id"} }

func (ownTracker) Item(context.Context, string) (map[string]string, error) { return nil, nil }

func (o ownTracker) ItemList(context.Context, string) ([]model.Item, error) { return o.items, o.err }

// ownRigWith is ownRig over the tracker items: the project names the tracker "github", whose adapter the test replaces.
func ownRigWith(t *testing.T, repo ownRepo, withWriter bool, items ownTracker) (ownGate, *[]model.CommitStatus) {
	t.Helper()

	project := model.Project{
		Name: "p", Source: repo.src, BaseBranch: "main", Gate: "sh scripts/check.sh", BuildHost: config.LocalHost, Forge: "github", Tracker: "github",
		GateProgram: []string{"scripts/"},
	}

	cfg := config.Config{Projects: map[string]config.Project{"p": {Project: project}}}
	posted := &[]model.CommitStatus{}
	pull := model.PullRequest{HeadRefOid: &repo.head, HeadRefName: ptrTo("gate-change"), BaseRefName: ptrTo("main")}

	table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(t.TempDir()), nil)
	table.forges = map[model.ForgeName]forgeAdapter{"github": {build: func(model.Project) (forge.Forge, error) { return ownForge{pull: pull, posted: posted}, nil }}}
	table.trackers = map[model.TrackerName]trackerAdapter{"github": {build: func(context.Context, model.Project) (store.Tracker, error) { return items, nil }}}

	own, err := ownGateOf(cfg, flow.Options{StateDir: t.TempDir()}, table, "p")
	require.NoError(t, err)

	if withWriter {
		own.project.Credentials = map[model.Role]model.Credential{model.RoleWriter: {}}
	}

	return own, posted
}

func ptrTo(s string) *string { return &s }

func TestOwnProgram_TheGateOfTheCommandHasNoGateProgramAndTheProjectKeepsIt(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)

	assert.Empty(t, own.gate.projects["p"].GateProgram, "the owner gate runs the program of the head with no overlay and no refusal")
	assert.Equal(t, []string{"scripts/"}, own.project.GateProgram, "the project that the command checks the paths against keeps its list")
}

func TestOwnProgram_ChangedListsEveryPathThatTheHeadChangesSinceTheMergeBase(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t, "code.txt", "docs/new file.md")
	own, _ := ownRig(t, repo, false)

	changed, err := own.changed(t.Context(), ownTip(t, own, "main"), repo.head)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"scripts/check.sh", "code.txt", "docs/new file.md"}, changed)
}

// ownTip reads the tip of the branch at the source of the owner gate.
func ownTip(t *testing.T, own ownGate, branch string) string {
	t.Helper()

	tip, err := own.tip(t.Context(), branch)
	require.NoError(t, err)

	return tip
}

// ownChain is the effects of the command over the real gate, with an owner who confirms every diff. shown collects the diffs that the owner saw.
func ownChain(own ownGate) (ownprogram.Deps, *[]string) {
	shown := &[]string{}

	return ownprogram.Deps{
		Pull: own.pull, OpenItems: own.openItems, Tip: own.tip, Changed: own.changed, Diff: own.diff, Judge: own.judge, Post: own.post,
		Confirm: func(_ context.Context, _ int, _, diff string) error {
			*shown = append(*shown, diff)

			return nil
		},
	}, shown
}

// ownCommit commits what the repo holds on its current branch, and returns the new head as the head of the repo.
func (r ownRepo) ownCommit(t *testing.T, message string) ownRepo {
	t.Helper()

	runGit(t, r.src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", message)

	return ownRepo{src: r.src, head: gitOut(t, r.src, "rev-parse", "HEAD")}
}

func TestOwnProgram_ARenameFromOutsideTheListIntoAListedPathIsRefusedAndNamesTheOldPath(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	runGit(t, repo.src, "mv", "code.txt", "scripts/code.txt")
	repo = repo.ownCommit(t, "rename")
	own, posted := ownRig(t, repo, true)
	deps, _ := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrOutsideProgram)
	assert.Contains(t, err.Error(), "code.txt")
	assert.Empty(t, *posted)
}

func TestOwnProgram_AHeadBehindTheBaseBranchIsRefusedAndAHeadThatMergedTheBaseIsNot(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	runGit(t, repo.src, "checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo.src, "code.txt"), []byte("moved\n"), 0o600))
	runGit(t, repo.src, "add", "code.txt")
	repo.ownCommit(t, "base moves")
	runGit(t, repo.src, "checkout", "-q", "gate-change")
	own, posted := ownRig(t, repo, true)
	deps, _ := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrBehindBase)
	assert.Empty(t, *posted)

	runGit(t, repo.src, "-c", "user.name=t", "-c", "user.email=t@example.com", "merge", "-q", "--no-edit", "main")

	merged := ownRepo{src: repo.src, head: gitOut(t, repo.src, "rev-parse", "HEAD")}
	own, _ = ownRig(t, merged, true)

	changed, err := own.changed(t.Context(), ownTip(t, own, "main"), merged.head)

	require.NoError(t, err)
	assert.Equal(t, []string{"scripts/check.sh"}, changed)
}

func TestOwnProgram_ASubmoduleUnderAListedPathIsRefused(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	runGit(t, repo.src, "update-index", "--add", "--cacheinfo", "160000,"+repo.head+",scripts/sub")
	repo = repo.ownCommit(t, "submodule")
	own, posted := ownRig(t, repo, true)
	deps, _ := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrSubmodule)
	assert.Contains(t, err.Error(), "scripts/sub")
	assert.Empty(t, *posted)
}

func TestOwnProgram_TheOwnerSeesTheDiffOfTheListedPathsBeforeTheGateRuns(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, true)
	deps, shown := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.NoError(t, err)
	require.Len(t, *shown, 1)
	assert.Contains(t, (*shown)[0], "+exit 0")
	assert.Contains(t, (*shown)[0], "-exit 1")
}

func TestOwnProgram_TheRawPathsOfAChangeRefuseAnEntryThatIsNotAMetaFieldAndAPath(t *testing.T) {
	t.Parallel()

	paths, err := rawPaths("")
	require.NoError(t, err)
	assert.Empty(t, paths)

	paths, err = rawPaths(":100644 100644 a b M\x00one.txt\x00:100644 100644 a b A\x00two.txt\x00")
	require.NoError(t, err)
	assert.Equal(t, []string{"one.txt", "two.txt"}, paths)

	for name, raw := range map[string]string{
		"a rename with two paths": ":100644 100644 a b R100\x00old.txt\x00new.txt\x00",
		"a meta field alone":      ":100644 100644 a b M\x00",
		"a short meta field":      ":100644 M\x00one.txt\x00",
		"a path first":            "one.txt\x00:100644 100644 a b M\x00",
	} {
		_, err := rawPaths(raw)
		require.Error(t, err, name)
	}
}

func TestOwnProgram_TheCommandRunsTheProgramOfTheHeadAndAsksThePostToSetTheGateStatus(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, posted := ownRig(t, repo, true)
	deps, _ := ownChain(own)

	out, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.NoError(t, err)
	assert.True(t, out.Passed, out.Report)
	require.Len(t, *posted, 1)
	assert.Equal(t, repo.head, (*posted)[0].SHA)
	assert.Equal(t, "beehave/gate", (*posted)[0].Context)
	assert.Equal(t, model.StatusSuccess, (*posted)[0].State)
	assert.Contains(t, (*posted)[0].Description, ownprogram.Description)
}

func TestOwnProgram_AChangeOutsideTheListedPathsRunsNoGateAndPostsNothing(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t, "code.txt")
	own, posted := ownRig(t, repo, true)

	deps, _ := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrOutsideProgram)
	assert.Contains(t, err.Error(), "code.txt")
	assert.Empty(t, *posted)
}

func TestOwnProgram_AProjectWithNoWriterRoleRunsTheGateAndPostsNothing(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, posted := ownRig(t, repo, false)

	deps, _ := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrNotPosted)
	assert.Contains(t, err.Error(), noWriterNote)
	assert.Empty(t, *posted)
}

func TestOwnProgram_TheWorktreeOfTheGateIsGoneWhenTheGateEnds(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)

	verdict, err := own.judge(t.Context(), repo.head)

	require.NoError(t, err)
	assert.True(t, verdict.Passed, verdict.Report)
	assert.Equal(t, repo.head, verdict.Head)
	assert.NoDirExists(t, filepath.Join(own.trees.root, "p", "own-"+repo.head[:ownTreeLength]))
}

func TestOwnProgram_ABuildWithNoForgeForTheProjectIsRefused(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}}

	_, err := ownDeps(cfg, flow.Options{StateDir: t.TempDir()}, newAdapters(proc.NewExecRunner("bd", ""), stateFolder(t.TempDir()), nil), "p")

	require.ErrorIs(t, err, forge.ErrNoForge)
}

func TestOwnProgram_AHeadWithNoMergeBaseWithTheBaseIsAnError(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	runGit(t, repo.src, "checkout", "-q", "--orphan", "unrelated")
	runGit(t, repo.src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "unrelated")

	own, _ := ownRig(t, repo, false)

	_, err := own.changed(t.Context(), ownTip(t, own, "unrelated"), repo.head)

	require.ErrorIs(t, err, ownprogram.ErrBehindBase)
}

func TestOwnProgram_ASourceThatCannotBeClonedOrNamedIsAnError(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)
	own.project.Source = filepath.Join(repo.src, "missing")

	_, err := own.tip(t.Context(), "main")
	require.Error(t, err)

	own.project.Source = ""
	own.forges = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	_, err = own.tip(t.Context(), "main")
	require.ErrorIs(t, err, forge.ErrNoForge)

	_, err = own.changed(t.Context(), repo.head, repo.head)
	require.ErrorIs(t, err, forge.ErrNoForge)

	fresh, _ := ownRig(t, repo, false)
	fresh.project.Source = filepath.Join(repo.src, "missing")

	_, err = fresh.trees.detach(t.Context(), fresh.project, "own-x", repo.head)
	require.Error(t, err, "a worktree needs a clone of the source")
}

func TestOwnProgram_TheBuildOfTheCommandRefusesAWriterThatCannotBeLoadedAndAProjectWithNoForge(t *testing.T) {
	t.Parallel()

	opts := flow.Options{StateDir: t.TempDir()}

	noForge := config.Config{Projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}}
	_, err := ownProgramFor(t.Context(), noForge, opts, "p")
	require.ErrorIs(t, err, forge.ErrNoForge)

	badWriter := config.Config{Projects: map[string]config.Project{"p": {Project: model.Project{
		Name: "p", Source: "/not/https", Credentials: map[model.Role]model.Credential{model.RoleWriter: {}},
	}}}}
	_, err = ownProgramFor(t.Context(), badWriter, opts, "p")
	require.Error(t, err)
}

func TestOwnProgram_AFailedReadOfTheBaseOrTheHeadIsAnError(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)

	_, err := own.tip(t.Context(), "no-such-branch")
	require.Error(t, err)

	tip := ownTip(t, own, "main")

	_, err = own.changed(t.Context(), tip, "ffffffffffffffffffffffffffffffffffffffff")
	require.Error(t, err)

	_, err = own.diff(t.Context(), "ffffffffffffffffffffffffffffffffffffffff", repo.head)
	require.Error(t, err)

	_, err = own.judge(t.Context(), "ffffffffffffffffffffffffffffffffffffffff")
	require.Error(t, err)
}

func TestOwnProgram_ADeadFolderForTheWorktreeAWorktreeThatIsNotThereAndADirtyWorktreeStopTheGate(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)

	_, err := own.run(t.Context(), "own-missing")
	require.ErrorIs(t, err, errNoGateFolder)

	dir, err := own.trees.detach(t.Context(), own.project, "own-dirty", repo.head)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o600))

	_, err = own.run(t.Context(), "own-dirty")
	require.ErrorIs(t, err, gate.ErrTreeChanged)

	require.NoError(t, os.RemoveAll(filepath.Join(own.trees.root, "p")))
	require.NoError(t, os.WriteFile(filepath.Join(own.trees.root, "p"), []byte("a file, not a folder"), 0o600))

	_, err = own.trees.detach(t.Context(), own.project, "own-blocked", repo.head)
	require.ErrorContains(t, err, "make the folder for the worktree")
}

func TestOwnProgram_TheBinaryHasTheGateCommand(t *testing.T) {
	t.Parallel()

	env := map[string]string{"HOME": t.TempDir()}

	var stdout, stderr bytes.Buffer

	code := cli.Execute(t.Context(), []string{"gate", "--help"}, buildInfo(env, "dev", flowFactory()), env, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Contains(t, stdout.String(), "beehave gate --own-program <pull request> [project]")
}

func TestOwnProgram_TheBuildOfTheCommandGivesEveryEffectOfTheDeps(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	project := model.Project{Name: "p", Source: repo.src, BaseBranch: "main", Forge: "github"}
	table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(t.TempDir()), nil)
	table.forges = map[model.ForgeName]forgeAdapter{"github": {build: func(model.Project) (forge.Forge, error) { return ownForge{}, nil }}}

	deps, err := ownDeps(config.Config{Projects: map[string]config.Project{"p": {Project: project}}}, flow.Options{StateDir: t.TempDir()}, table, "p")

	require.NoError(t, err)
	assert.NotNil(t, deps.Pull)
	assert.NotNil(t, deps.OpenItems)
	assert.NotNil(t, deps.Tip)
	assert.NotNil(t, deps.Changed)
	assert.NotNil(t, deps.Judge)
	assert.NotNil(t, deps.Post)
}

func TestOwnProgram_TheOpenItemOfAGithubTrackerThatNeverRanIsRefusedThroughTheTrackerOfTheProject(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, posted := ownRigWith(t, repo, true, ownTracker{items: []model.Item{{ID: "gate-change"}}})
	deps, shown := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrItemBranch)
	assert.Empty(t, *shown)
	assert.Empty(t, *posted)
}

func TestOwnProgram_ATrackerThatCannotListItsItemsStopsTheCommandBeforeTheGate(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, posted := ownRigWith(t, repo, true, ownTracker{err: errors.New("store: cannot list the open issues; refusing")})
	deps, shown := ownChain(own)

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorContains(t, err, "cannot list the open issues")
	assert.Empty(t, *shown)
	assert.Empty(t, *posted)
}

// moveBase commits a change of code.txt on main at the source, and goes back to the head branch.
func moveBase(t *testing.T, repo ownRepo) {
	t.Helper()

	runGit(t, repo.src, "checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo.src, "code.txt"), []byte("moved while the gate ran\n"), 0o600))
	runGit(t, repo.src, "add", "code.txt")
	repo.ownCommit(t, "base moves")
	runGit(t, repo.src, "checkout", "-q", "gate-change")
}

func TestOwnProgram_ABaseThatMovesWhileTheGateRunsPostsNothing(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, posted := ownRig(t, repo, true)
	deps, _ := ownChain(own)
	deps.Judge = func(ctx context.Context, head string) (ownprogram.Verdict, error) {
		verdict, err := own.judge(ctx, head)
		moveBase(t, repo) //nolint:contextcheck // the test helpers of git take the test, not a context

		return verdict, err
	}

	_, err := ownprogram.Run(t.Context(), deps, ownprogram.Request{Project: own.project.Project, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrMoved)
	assert.Contains(t, err.Error(), "the base branch main moved")
	assert.Empty(t, *posted)
}

func TestOwnProgram_AGateThatCannotKeepItsFolderOrReadItsKeyStopsTheCommand(t *testing.T) {
	t.Parallel()

	repo := newOwnRepo(t)
	own, _ := ownRig(t, repo, false)

	// A worktree folder that is already there makes the add refuse.
	dir := filepath.Join(own.trees.root, "p", "own-"+repo.head[:ownTreeLength])
	require.NoError(t, os.MkdirAll(dir, 0o700))

	_, err := own.judge(t.Context(), repo.head)

	require.Error(t, err)
}
