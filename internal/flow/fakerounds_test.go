package flow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/roundcut"
	"github.com/tobocop2/beehest/internal/roundintegration"
	"github.com/tobocop2/beehest/internal/roundsync"
	"github.com/tobocop2/beehest/internal/takeover"
)

// fakeRounds is the round writer of a test. It runs no forge: each call is written as the line of the engine step that it stands for, and goes to the fake commands, whose reply says what the
// forge answers. So a test scripts one answer for the round and reads one list of calls. A reply with exit code 2 is a refusal.
type fakeRounds struct{ commands *fakeCommands }

// The programs of the lines that the fake writes for the engine steps of the round.
const (
	syncProgram        = "round_sync"
	landProgram        = "round_land"
	cutProgram         = "round_cut"
	integrationProgram = "integration_pull"
	boardProgram       = "board_card"
	takeProgram        = "pr_take"
)

// ask records the call as the line of the program and returns the answer of the fake commands.
func (f fakeRounds) ask(ctx context.Context, program string, words ...string) (CommandOutput, error) {
	return f.commands.Run(ctx, CommandRequest{Run: append([]string{program}, words...)})
}

// lineFields reads the name=value words of a line.
func lineFields(line string) map[string]string {
	found := map[string]string{}

	for _, word := range strings.Fields(line) {
		if name, value, ok := strings.Cut(word, "="); ok {
			found[name] = value
		}
	}

	return found
}

func (f fakeRounds) Sync(ctx context.Context, _ string, req roundsync.Request) (roundsync.Result, error) {
	out, err := f.ask(ctx, syncProgram, "--base", req.Base, "--trunk", req.Trunk)

	return syncResult(out, err)
}

func (f fakeRounds) Land(ctx context.Context, _ string, req roundsync.LandRequest) (roundsync.Result, error) {
	out, err := f.ask(ctx, landProgram, "--pr", strconv.Itoa(req.PR), "--head", req.Head, "--base-tip", req.BaseTip)

	return syncResult(out, err)
}

// syncResult reads the line of a sync answer. A line with no state is current, as the old round read it.
func syncResult(out CommandOutput, err error) (roundsync.Result, error) {
	if err != nil {
		return roundsync.Result{}, err
	}

	words := lineFields(out.Stdout)
	if out.RC != 0 && words["state"] == "" {
		return roundsync.Result{}, fmt.Errorf("the sync ended with the exit code %d: %s", out.RC, out.Stdout)
	}

	pr, err := numberOf(words["pr"])
	if err != nil {
		return roundsync.Result{}, err
	}

	return roundsync.Result{
		State: roundsync.State(cmpOr(words["state"], string(roundsync.Current))), Branch: words["branch"], Source: words["source"], PR: pr,
		Head: words["head"], BaseTip: words["base_tip"], Reason: words["reason"],
	}, nil
}

// numberOf reads a number that a scripted reply names, or 0 when the reply names none.
func numberOf(text string) (int, error) {
	if text == "" {
		return 0, nil
	}

	return strconv.Atoi(text)
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func (f fakeRounds) Cut(ctx context.Context, _ string, req roundcut.Request) (roundcut.Result, error) {
	out, err := f.ask(ctx, cutProgram, "--old", req.Cut.OldBase, "--new", req.Cut.NewBase, "--pr", strconv.Itoa(req.Cut.OldPR), "--trunk", req.Cut.Trunk)
	if err != nil {
		return roundcut.Result{}, err
	}

	if out.RC == refusedRC {
		return roundcut.Result{Stop: roundcut.Refused, Lines: []string{strings.TrimSpace(out.Stdout)}}, nil
	}

	return roundcut.Result{Stop: roundcut.Done}, nil
}

func (f fakeRounds) Integrate(ctx context.Context, _ string, req roundintegration.Request) (roundintegration.Result, error) {
	out, err := f.ask(ctx, integrationProgram, "--base", req.Base, "--into", req.Into, "--pr", strconv.Itoa(req.PR))
	if err != nil {
		return roundintegration.Result{}, err
	}

	words := lineFields(out.Stdout)
	pr, err := numberOf(words["pr"])
	if err != nil {
		return roundintegration.Result{}, err
	}

	return roundintegration.Result{PR: pr, State: words["state"]}, nil
}

// Place answers the column that the fake commands print in stdout. A reply with exit code 2 shows the column that the reply names as "shows", else the empty column.
func (f fakeRounds) Place(ctx context.Context, _ string, card model.Card) (string, error) {
	words := []string{"--board", strconv.Itoa(card.Board), "--status", card.Status}
	if card.Owner != "" {
		words = append(words, "--owner", card.Owner)
	}

	if card.Pull {
		words = append(words, "--pr", strconv.Itoa(card.Number))
	} else {
		words = append(words, "--issue", strconv.Itoa(card.Number))
	}

	out, err := f.ask(ctx, boardProgram, words...)
	if err != nil {
		return "", err
	}

	if out.RC == refusedRC {
		return "Todo", nil
	}

	return card.Status, nil
}

func (f fakeRounds) Take(ctx context.Context, _ string, req TakeRequest) (takeover.Result, error) {
	words := []string{strconv.Itoa(req.Request.PR), "--me", req.Me, "--idle", wholeHours(req.Idle), "--grace", wholeHours(req.Grace)}

	switch req.Request.Mode {
	case takeover.ModeApply:
		words = append(words, "--apply")
	case takeover.ModeTake:
		words = append(words, "--take")
	case takeover.ModeDry:
	}

	out, err := f.ask(ctx, takeProgram, words...)
	if err != nil {
		return takeover.Result{}, err
	}

	return takeover.Result{Code: out.RC, Lines: []string{strings.TrimSpace(out.Stdout)}}, nil
}

func wholeHours(d time.Duration) string { return strconv.Itoa(int(d / time.Hour)) }
