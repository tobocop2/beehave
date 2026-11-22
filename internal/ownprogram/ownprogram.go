// Package ownprogram is the owner command that runs the gate of one pull request with the program of its own head and posts the result.
package ownprogram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/model"
)

// Description starts the text of the status that the command posts.
const Description = "gate program changed, run by owner command"

// ErrItemBranch is the cause when the branch of the pull request is, or starts with, the branch of an item of the project.
var ErrItemBranch = errors.New("the pull request is on the branch of an item")

// ErrOutsideProgram is the cause when the pull request changes a path that gate_program does not list.
var ErrOutsideProgram = errors.New("the pull request changes a path outside gate_program")

// ErrUnreadPull is the cause when the forge does not tell the head, the branch or the base of the pull request.
var ErrUnreadPull = errors.New("the forge did not tell the head, the branch and the base of the pull request")

// ErrNotPosted is the cause when the gate ran and no status was posted.
var ErrNotPosted = errors.New("the gate result was not posted")

// ErrWrongBase is the cause when the pull request does not target the base branch of the project.
var ErrWrongBase = errors.New("the pull request does not target the base branch of the project")

// ErrBehindBase is the cause when the head does not contain the tip of the base branch.
var ErrBehindBase = errors.New("the head is behind the base branch")

// ErrSubmodule is the cause when the pull request changes a submodule.
var ErrSubmodule = errors.New("the pull request changes a submodule")

// ErrNotConfirmed is the cause when the owner did not confirm the diff.
var ErrNotConfirmed = errors.New("the owner did not confirm the diff")

// ErrMoved is the cause when the head of the pull request or the tip of the base branch moved while the gate ran.
var ErrMoved = errors.New("the pull request moved while the gate ran")

// Request is one run of the command. The base branch of the project is Project.BaseBranch. ItemBranches are the branches of the items that the runs of
// the project name; the command adds the branches of the open items of the tracker.
type Request struct {
	Project      model.Project
	PR           int
	ItemBranches []string
}

// Verdict is what the gate said about the head. Key is the gate key, or empty when the gate has none.
type Verdict struct {
	Head   string
	Key    string
	Passed bool
	Report string
}

// Outcome is the result of a run that posted its status.
type Outcome struct {
	Head   string
	Passed bool
	Report string
}

// Deps are the effects of the command. OpenItems lists the open items of the tracker of the project. Tip reads the commit at the tip of the base branch.
// Changed lists the paths that head changes against tip, and refuses a head that does not contain tip or that changes a submodule. Diff is the text of
// that change. Confirm shows the diff to the owner and returns an error unless the owner confirms it. Judge runs the gate on head with the program of that
// head and no overlay. Post sets the statuses and returns a note when it posted none.
type Deps struct {
	Pull      func(ctx context.Context, pr int) (model.PullRequest, error)
	OpenItems func(ctx context.Context) ([]model.Item, error)
	Tip       func(ctx context.Context, base string) (string, error)
	Changed   func(ctx context.Context, tip, head string) ([]string, error)
	Diff      func(ctx context.Context, tip, head string) (string, error)
	Confirm   func(ctx context.Context, pr int, head, diff string) error
	Judge     func(ctx context.Context, head string) (Verdict, error)
	Post      func(ctx context.Context, statuses []model.CommitStatus) (string, error)
}

// point is the head of the pull request and the tip of the base branch that the command checked and showed.
type point struct {
	head string
	tip  string
}

// Run refuses an item branch, a base that is not the base of the project and a change outside gate_program. Then it shows the diff to the owner, and after
// the owner confirms it judges the head with its own program. It posts the verdict only when the head and the tip of the base are where they were.
func Run(ctx context.Context, deps Deps, request Request) (Outcome, error) {
	at, err := admit(ctx, deps, request)
	if err != nil {
		return Outcome{}, err
	}

	if err := confirm(ctx, deps, request, at); err != nil {
		return Outcome{}, err
	}

	verdict, err := deps.Judge(ctx, at.head)
	if err != nil {
		return Outcome{}, fmt.Errorf("run the gate of pull request %d: %w", request.PR, err)
	}

	if err := unmoved(ctx, deps, request, at); err != nil {
		return Outcome{}, err
	}

	return post(ctx, deps, verdict)
}

// admit reads the pull request and returns its head and the tip of the base, or the refusal of a pull request that the command must not judge.
func admit(ctx context.Context, deps Deps, request Request) (point, error) {
	pull, err := readPull(ctx, deps, request)
	if err != nil {
		return point{}, err
	}

	if err := refuseItemBranch(ctx, deps, request, *pull.HeadRefName); err != nil {
		return point{}, err
	}

	tip, err := deps.Tip(ctx, request.Project.BaseBranch)
	if err != nil {
		return point{}, fmt.Errorf("read the tip of the base branch %s: %w", request.Project.BaseBranch, err)
	}

	at := point{head: *pull.HeadRefOid, tip: tip}

	return at, checkPaths(ctx, deps, request, at)
}

// readPull reads the pull request, and refuses one that the forge does not describe or that does not target the base branch of the project.
func readPull(ctx context.Context, deps Deps, request Request) (model.PullRequest, error) {
	pull, err := deps.Pull(ctx, request.PR)
	if err != nil {
		return model.PullRequest{}, fmt.Errorf("read pull request %d: %w", request.PR, err)
	}

	if pull.HeadRefOid == nil || pull.HeadRefName == nil || pull.BaseRefName == nil {
		return model.PullRequest{}, fmt.Errorf("%w: pull request %d", ErrUnreadPull, request.PR)
	}

	if *pull.BaseRefName != request.Project.BaseBranch {
		return model.PullRequest{}, fmt.Errorf("%w: pull request %d targets %s and the base branch is %s. A status stands for the commit on every pull request",
			ErrWrongBase, request.PR, *pull.BaseRefName, request.Project.BaseBranch)
	}

	return pull, nil
}

// refuseItemBranch refuses a head branch on the branch of an item of the project: an item that the runs name, or an open item of the tracker. A list that
// the tracker cannot give stops the command, so no item branch passes for want of a list.
func refuseItemBranch(ctx context.Context, deps Deps, request Request, branch string) error {
	items, err := deps.OpenItems(ctx)
	if err != nil {
		return fmt.Errorf("read the open items of the project, to refuse the branch of an item: %w", err)
	}

	branches := slices.Clone(request.ItemBranches)
	for _, item := range items {
		branches = append(branches, model.ItemBranch(item.ID))
	}

	if onItemBranch(branch, branches) {
		return fmt.Errorf("%w: pull request %d is on branch %s. The normal gate judges an item", ErrItemBranch, request.PR, branch)
	}

	return nil
}

// unmoved reads the pull request and the tip of the base again, and refuses to post when the head or the tip is not the one that the gate judged.
func unmoved(ctx context.Context, deps Deps, request Request, at point) error {
	pull, err := readPull(ctx, deps, request)
	if err != nil {
		return err
	}

	if *pull.HeadRefOid != at.head {
		return fmt.Errorf("%w: the head moved from %s to %s. Nothing was posted; run the command again", ErrMoved, at.head, *pull.HeadRefOid)
	}

	tip, err := deps.Tip(ctx, request.Project.BaseBranch)
	if err != nil {
		return fmt.Errorf("read the tip of the base branch %s again: %w", request.Project.BaseBranch, err)
	}

	if tip != at.tip {
		return fmt.Errorf("%w: the base branch %s moved from %s to %s. Nothing was posted; update the branch, then run the command again",
			ErrMoved, request.Project.BaseBranch, at.tip, tip)
	}

	return nil
}

// onItemBranch says whether a head branch is, or starts with, the branch of an item. It ignores case, the spaces around the name and a refs/heads/ prefix.
// A branch that starts with the name of an item, and then has a character that is no letter or digit, counts too: bh-12-gate for the item bh-12.
func onItemBranch(branch string, items []string) bool {
	name := strings.ToLower(strings.TrimSpace(branch))
	name = strings.TrimPrefix(strings.TrimPrefix(name, "refs/"), "heads/")

	return slices.ContainsFunc(items, func(item string) bool {
		item = strings.ToLower(strings.TrimSpace(item))
		rest, found := strings.CutPrefix(name, item)

		next, _ := utf8.DecodeRuneInString(rest)

		return item != "" && found && !unicode.IsLetter(next) && !unicode.IsDigit(next)
	})
}

// checkPaths refuses a head that changes a path that gate_program does not list. It compares with the tip of the base branch of the project.
func checkPaths(ctx context.Context, deps Deps, request Request, at point) error {
	changed, err := deps.Changed(ctx, at.tip, at.head)
	if err != nil {
		return fmt.Errorf("list the paths that pull request %d changes: %w", request.PR, err)
	}

	if outside := outsideOf(request.Project.GateProgram, changed); len(outside) > 0 {
		return fmt.Errorf("%w: %s. Move these changes to another pull request, so the owner reads the whole diff", ErrOutsideProgram, strings.Join(outside, " "))
	}

	return nil
}

// confirm shows the diff of the head against the same tip to the owner before the gate runs. The gate runs the program of the head, so the owner reads it
// first.
func confirm(ctx context.Context, deps Deps, request Request, at point) error {
	diff, err := deps.Diff(ctx, at.tip, at.head)
	if err != nil {
		return fmt.Errorf("read the diff of pull request %d: %w", request.PR, err)
	}

	if deps.Confirm == nil {
		return fmt.Errorf("%w: pull request %d", ErrNotConfirmed, request.PR)
	}

	return deps.Confirm(ctx, request.PR, at.head, diff)
}

// post sets the status of the verdict on the head that the gate judged: success for a pass, failure for any other result.
func post(ctx context.Context, deps Deps, verdict Verdict) (Outcome, error) {
	state := model.StatusFailure
	if verdict.Passed {
		state = model.StatusSuccess
	}

	note, err := deps.Post(ctx, []model.CommitStatus{{SHA: verdict.Head, Context: batch.ContextGate, State: state, Description: describe(verdict.Key)}})
	if err != nil {
		return Outcome{}, err
	}

	if note != "" {
		return Outcome{}, fmt.Errorf("%w: %s", ErrNotPosted, note)
	}

	return Outcome{Head: verdict.Head, Passed: verdict.Passed, Report: verdict.Report}, nil
}

// describe is the description of the status: the fixed text, and the gate key when the gate has one.
func describe(key string) string {
	if key == "" {
		return Description
	}

	return Description + ", gate key " + key
}

// outsideOf lists the paths that no entry of program covers. An entry that ends in a slash covers every path below it.
func outsideOf(program, changed []string) []string {
	var outside []string

	for _, path := range changed {
		covered := slices.ContainsFunc(program, func(entry string) bool {
			return path == entry || (strings.HasSuffix(entry, "/") && strings.HasPrefix(path, entry))
		})
		if !covered {
			outside = append(outside, path)
		}
	}

	return outside
}
