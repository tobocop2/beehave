package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/fstree"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/ownprogram"
	"github.com/tobocop2/beehest/internal/proc"
)

// ownTreeLength is the number of characters of the head that name the worktree of an owner gate.
const ownTreeLength = 12

// ownProgramFor wires the owner command of one project over the real adapters.
func ownProgramFor(ctx context.Context, cfg config.Config, opts flow.Options, project string) (ownprogram.Deps, error) {
	table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), opts.Env)
	if err := table.writers.start(ctx, cfg, opts); err != nil {
		return ownprogram.Deps{}, err
	}

	return ownDeps(cfg, opts, table, project)
}

// ownGate is the effects of the owner command for one project: the forge of the project, its clone and the gate that runs with the program of the head.
type ownGate struct {
	project config.Project
	forges  func(model.Project) (forge.Forge, error)
	forge   forge.Forge
	trees   worktrees
	// items reads the open items through the tracker adapter of the project, the read that the queue makes.
	items queueReads
	// gate is the gate of the project with gate_program empty, so it runs the program of the head with no overlay and no refusal.
	gate projectGate
}

// ownDeps builds the effects of the owner command for the project over a table of adapters.
func ownDeps(cfg config.Config, opts flow.Options, table adapters, project string) (ownprogram.Deps, error) {
	own, err := ownGateOf(cfg, opts, table, project)
	if err != nil {
		return ownprogram.Deps{}, err
	}

	return ownprogram.Deps{
		Pull: own.pull, OpenItems: own.openItems, Tip: own.tip, Changed: own.changed, Diff: own.diff, Judge: own.judge, Post: own.post,
	}, nil
}

// ownGateOf builds the owner gate of the project over a table of adapters.
func ownGateOf(cfg config.Config, opts flow.Options, table adapters, project string) (ownGate, error) {
	found := cfg.Projects[project]

	built, err := table.forge(found.Project)
	if err != nil {
		return ownGate{}, fmt.Errorf("build the forge of project %s: %w", project, err)
	}

	trees := newWorktrees(cfg.Projects, opts, table.forge, table.writers)
	commands := newPrograms(cfg, trees, cloneLock{repos: trees.repos}, table, &fstree.Folders{})

	programless := maps.Clone(cfg.Projects)
	off := found
	off.GateProgram = nil
	programless[project] = off

	return ownGate{
		project: found, forges: table.forge, forge: built, trees: trees,
		items: queueReads{items: trackerItems{projects: cfg.Projects, tracker: table.tracker}},
		gate:  projectGate{projects: programless, programs: commands, forge: table.forge, tip: trees.BaseTip, fetch: trees.FetchCommit},
	}, nil
}

// pull reads the head, the branch and the base of the pull request.
func (o ownGate) pull(ctx context.Context, pr int) (model.PullRequest, error) {
	return o.forge.PRView(ctx, pr, "headRefOid", "headRefName", "baseRefName")
}

// openItems lists the open items of the tracker of the project.
func (o ownGate) openItems(ctx context.Context) ([]model.Item, error) {
	return o.items.ItemList(ctx, o.project.Name, "")
}

// tip makes the clone of the project when it is missing and reads the commit at the tip of the base branch at the source of the project.
func (o ownGate) tip(ctx context.Context, base string) (string, error) {
	remote, err := cloneSource(o.project.Project, o.forges)
	if err != nil {
		return "", err
	}

	if err := (cloneLock{repos: o.trees.repos}).hold(ctx, o.project.Name, func(clone string) error { return o.trees.clone(ctx, o.project, clone) }); err != nil {
		return "", err
	}

	tip, err := o.trees.BaseTip(ctx, o.project.Name, remote, base)
	if err != nil {
		return "", fmt.Errorf("read the base branch %q: %w", base, err)
	}

	return tip, nil
}

// changed lists the paths that head changes against tip, the tip of the base branch. It reads git, not the file list of the forge: that list is cut short on a
// large pull request. A rename shows as a delete of the old path and an add of the new path, so no path hides in it.
// It refuses a head that does not contain the tip, so a gate never runs on a head that is behind the base, and a head that changes a submodule.
func (o ownGate) changed(ctx context.Context, tip, head string) ([]string, error) {
	git, err := o.read(ctx, tip, head)
	if err != nil {
		return nil, err
	}

	if err := o.contains(ctx, git, tip, head); err != nil {
		return nil, err
	}

	raw := git.Run(ctx, []string{"diff", "--no-renames", "--raw", "-z", tip + "..." + head})
	paths, err := rawPaths(raw.Stdout)

	return paths, errors.Join(gitExit("diff", o.project.Name, raw), err)
}

// diff is the text of the change of head against tip, the tip of the base branch.
func (o ownGate) diff(ctx context.Context, tip, head string) (string, error) {
	git, err := o.read(ctx, tip, head)
	if err != nil {
		return "", err
	}

	text := git.Run(ctx, []string{"diff", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv", tip + "..." + head})

	return text.Stdout, gitExit("diff", o.project.Name, text)
}

// read fetches the head and the tip from the source of the project into its clone, and returns the runner of the clone.
func (o ownGate) read(ctx context.Context, tip, head string) (proc.Runner, error) { //nolint:ireturn // the trees contract of the engine returns the runner interface
	remote, err := cloneSource(o.project.Project, o.forges)
	if err != nil {
		return nil, err
	}

	for _, sha := range []string{head, tip} {
		if err := o.trees.FetchCommit(ctx, o.project.Name, remote, sha); err != nil {
			return nil, fmt.Errorf("fetch the commit %s: %w", sha, err)
		}
	}

	return o.trees.Git(filepath.Join(o.trees.repos, model.SafeName(o.project.Name))), nil
}

// contains refuses a head that does not contain the tip. A head behind the base was never gated over the code that the base has now.
func (o ownGate) contains(ctx context.Context, git proc.Runner, tip, head string) error {
	out := git.Run(ctx, []string{"merge-base", "--is-ancestor", tip, head})
	if out.ExitCode == 1 {
		return fmt.Errorf("%w: update the branch with the base branch, then run the command again", ownprogram.ErrBehindBase)
	}

	return gitExit("merge-base", o.project.Name, out)
}

// gitlinkMode is the mode that git gives to a submodule entry.
const gitlinkMode = "160000"

// rawPaths reads the paths of the output of git diff --raw -z. An entry that is not one meta field and one path, or that holds a submodule, is an error.
func rawPaths(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}

	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")

	var paths []string

	for i := 0; i < len(fields); i += 2 {
		path, err := rawPath(fields, i)
		if err != nil {
			return nil, err
		}

		paths = append(paths, path)
	}

	return paths, nil
}

// rawPath returns the path of the entry that starts at fields[i], the meta field, and refuses an entry of a submodule.
func rawPath(fields []string, i int) (string, error) {
	meta := strings.Fields(fields[i])
	if i+1 >= len(fields) || len(meta) < rawMetaFields || !strings.HasPrefix(meta[0], ":") {
		return "", fmt.Errorf("read the paths of the change: unexpected entry %q", fields[i])
	}

	if meta[0] == ":"+gitlinkMode || meta[1] == gitlinkMode {
		return "", fmt.Errorf("%w: %s. The owner cannot read a submodule in the diff", ownprogram.ErrSubmodule, fields[i+1])
	}

	return fields[i+1], nil
}

// rawMetaFields is the count of fields of the meta part of a raw entry: two modes, two ids and the status.
const rawMetaFields = 5

// judge runs the gate of the project on a detached worktree of head with the program of that head, and removes the worktree after.
func (o ownGate) judge(ctx context.Context, head string) (ownprogram.Verdict, error) {
	name := "own-" + head[:min(len(head), ownTreeLength)]

	dir, err := o.trees.detach(ctx, o.project, name, head)
	if err != nil {
		return ownprogram.Verdict{}, err
	}

	verdict, err := o.run(ctx, name)

	return verdict, errors.Join(err, o.trees.drop(ctx, o.project.Name, dir))
}

// run reads the key of the gate on the worktree and runs the gate.
func (o ownGate) run(ctx context.Context, name string) (ownprogram.Verdict, error) {
	key, err := o.gate.Key(ctx, o.project.Name, name, "", "", "")
	if err != nil {
		return ownprogram.Verdict{}, err
	}

	result, err := o.gate.Run(ctx, o.project.Name, name, "", flow.GateJob{})
	if err != nil {
		return ownprogram.Verdict{}, err
	}

	return ownprogram.Verdict{Head: result.Head, Key: key.ID, Passed: result.Passed, Report: result.Report}, nil
}

// post sets the statuses as the writer role of the project.
func (o ownGate) post(ctx context.Context, statuses []model.CommitStatus) (string, error) {
	return posting(o.project.Project, o.forge, statuses).Post(ctx)
}

// detach adds a detached worktree of the project at sha, named name, and returns its folder.
func (w worktrees) detach(ctx context.Context, project config.Project, name, sha string) (string, error) {
	dir := filepath.Join(w.root, model.SafeName(project.Name), model.SafeName(name))

	err := cloneLock{repos: w.repos}.hold(ctx, project.Name, func(clone string) error {
		if err := w.clone(ctx, project, clone); err != nil {
			return err
		}

		if err := absent(dir); err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(dir), folderMode); err != nil {
			return fmt.Errorf("make the folder for the worktree %s: %w", name, err)
		}

		return w.run(ctx, clone, gitWorktree, gitAdd, "--force", "--detach", dir, sha)
	})

	return dir, err
}

// drop removes the worktree dir that detach added.
func (w worktrees) drop(ctx context.Context, project, dir string) error {
	return cloneLock{repos: w.repos}.hold(ctx, project, func(clone string) error {
		return w.run(ctx, clone, gitWorktree, "remove", "--force", dir)
	})
}
