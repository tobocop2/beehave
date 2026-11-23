package roundcut_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	tip    = "1111111111111111111111111111111111111111"
	other  = "2222222222222222222222222222222222222222"
	moved  = "3333333333333333333333333333333333333333"
	oldPR  = 5
	oldRef = "staging/remediation-V"
	newRef = "staging/remediation-VI"
)

var errDown = errors.New("down")

// pull is one pull request of the fake forge.
type pull struct {
	number int
	title  string
	base   string
	closed bool
}

// world is a stateful fake forge. A write changes its branches and bases. Every write is recorded in writes.
// failWrite is the number of the write that fails (1 is the first), and noWrites makes every write fail.
// stuck lists pull requests that a base change does not move. fails names a read that fails, and failList the list call that fails.
type world struct {
	state     string
	mergedAt  *string
	trunk     string
	trunkOK   bool
	branches  map[string]string
	pulls     []pull
	stuck     map[int]bool
	fails     map[string]bool
	failList  int
	failWrite int
	noWrites  bool
	writes    []string
	lists     [][]string

	// afterTip runs once, right after the first read of the trunk tip. afterList runs after each list.
	afterTip  func()
	tipReads  int
	afterList func()
}

func newWorld() *world {
	at := "2026-10-01T00:00:00Z"

	return &world{
		state: "MERGED", mergedAt: &at, trunk: tip, trunkOK: true,
		branches: map[string]string{}, stuck: map[int]bool{}, fails: map[string]bool{},
		pulls: []pull{{11, "one", oldRef, false}, {12, "two", oldRef, false}},
	}
}

func (w *world) PRView(_ context.Context, number int, fields ...string) (model.PullRequest, error) {
	if w.fails["PRView"] {
		return model.PullRequest{}, fmt.Errorf("view: %w", errDown)
	}

	if number != oldPR {
		return w.viewRow(number, fields)
	}

	if !slices.Equal(fields, []string{"state", "mergedAt", "baseRefName"}) {
		return model.PullRequest{}, fmt.Errorf("unexpected view of #%d %v: %w", number, fields, errDown)
	}

	base := "main"

	return model.PullRequest{Number: number, State: &w.state, MergedAt: w.mergedAt, BaseRefName: &base}, nil
}

// viewRow answers the second read of a row: its state and its base.
func (w *world) viewRow(number int, fields []string) (model.PullRequest, error) {
	if !slices.Equal(fields, []string{"state", "baseRefName"}) || w.fails["row"] {
		return model.PullRequest{}, fmt.Errorf("unexpected view of #%d %v: %w", number, fields, errDown)
	}

	for _, p := range w.pulls {
		if p.number == number {
			state := "OPEN"
			if p.closed {
				state = "CLOSED"
			}

			return model.PullRequest{Number: number, State: &state, BaseRefName: &p.base}, nil
		}
	}

	return model.PullRequest{}, fmt.Errorf("no pull request %d: %w", number, errDown)
}

func (w *world) PRList(_ context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	w.lists = append(w.lists, append([]string{strconv.Itoa(limit)}, args...))

	if w.failList == len(w.lists) {
		return nil, fmt.Errorf("cannot list pull requests (%s): %w", strings.Join(args, " "), errDown)
	}

	at := slices.Index(args, "--base")

	var rows []model.PullRequest

	for _, p := range w.pulls {
		if at >= 0 && p.base == args[at+1] {
			rows = append(rows, model.PullRequest{Number: p.number, Title: p.title})
		}
	}

	if w.afterList != nil {
		w.afterList()
	}

	return rows, nil
}

func (w *world) BranchRef(_ context.Context, branch string) (sha string, found bool, err error) {
	if branch == "main" {
		sha, found, err = w.trunk, w.trunkOK, w.failIf("trunk")

		w.tipReads++
		if w.tipReads == 1 && w.afterTip != nil {
			w.afterTip()
		}

		return sha, found, err
	}

	if err := w.failIf("branch"); err != nil {
		return "", false, err
	}

	sha, found = w.branches[branch]

	return sha, found, nil
}

func (w *world) failIf(name string) error {
	if w.fails[name] {
		return fmt.Errorf("%s: %w", name, errDown)
	}

	return nil
}

// write records one write and says whether it may go on.
func (w *world) write(line string) error {
	w.writes = append(w.writes, line)

	if w.noWrites || w.failWrite == len(w.writes) {
		return fmt.Errorf("%s: %w", line, errDown)
	}

	return nil
}

func (w *world) CreateBranch(_ context.Context, branch, sha string) error {
	if err := w.write("CreateBranch " + branch + " " + sha); err != nil {
		return err
	}

	w.branches[branch] = sha

	return nil
}

func (w *world) SetBase(_ context.Context, number int, base string) error {
	if err := w.write(fmt.Sprintf("SetBase %d %s", number, base)); err != nil {
		return err
	}

	for i := range w.pulls {
		if w.pulls[i].number == number && !w.stuck[number] {
			w.pulls[i].base = base
		}
	}

	return nil
}
