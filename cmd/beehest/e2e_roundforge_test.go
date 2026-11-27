package main

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
)

// The methods of this file are the write sides of the forge that a round needs (the sync, the cut and the board). The scripts of a round ran these through a fake remedy tool;
// the engine now calls them on the forge, so the stand-in forge does them on the bare repository.

// CreateBranch makes the branch at sha.
func (f swarmForgeAt) CreateBranch(ctx context.Context, branch, sha string) error {
	_, err := f.git(ctx, "update-ref", "refs/heads/"+branch, sha)

	return err
}

// CommitsAhead counts the commits of head that base lacks.
func (f swarmForgeAt) CommitsAhead(ctx context.Context, base, head string) (int, error) {
	out, err := f.git(ctx, "rev-list", "--count", "refs/heads/"+base+"..refs/heads/"+head)
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(out)
}

// MergeBranch merges head into base with a merge commit in the bare repository and returns the commit.
func (f swarmForgeAt) MergeBranch(ctx context.Context, base, head string) (string, error) {
	tip, err := f.git(ctx, "rev-parse", "refs/heads/"+base)
	if err != nil {
		return "", err
	}

	other, err := f.git(ctx, "rev-parse", "refs/heads/"+head)
	if err != nil {
		return "", err
	}

	tree, err := f.git(ctx, "merge-tree", "--write-tree", tip, other)
	if err != nil {
		return "", err
	}

	tree, _, _ = strings.Cut(tree, "\n")

	commit, err := f.git(ctx, "commit-tree", tree, "-p", tip, "-p", other, "-m", "merge")
	if err != nil {
		return "", err
	}

	_, err = f.git(ctx, "update-ref", "refs/heads/"+base, commit)

	return commit, err
}

// SetDraft changes nothing: the stand-in forge keeps no draft state.
func (swarmForgeAt) SetDraft(context.Context, int, bool) error { return nil }

// ClosePR changes nothing: the stand-in forge keeps no closed state.
func (swarmForgeAt) ClosePR(context.Context, int, string) error { return nil }

// PlaceCard counts the call in the file board-calls of the forge folder.
func (f swarmForgeAt) PlaceCard(_ context.Context, _ model.Card) error {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	log, err := os.OpenFile(f.path("board-calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, e2eFileMode)
	if err != nil {
		return err
	}

	_, err = log.WriteString("PlaceCard\n")

	return errJoin(err, log.Close())
}

// CardStatus shows the column of the card. If the file board-wrong exists in the forge folder, the first card call shows the column that the file names, as a board that did not move the card does.
func (f swarmForgeAt) CardStatus(_ context.Context, card model.Card) (string, error) {
	swarmLock.Lock()
	defer swarmLock.Unlock()

	calls, err := os.ReadFile(f.path("board-calls"))
	if err != nil {
		return "", err
	}

	wrong, err := os.ReadFile(f.path("board-wrong"))
	if err == nil && strings.Count(string(calls), "PlaceCard") == 1 {
		return strings.TrimSpace(string(wrong)), nil
	}

	return card.Status, nil
}
