package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/roundcut"
	"github.com/tobocop2/beehave/internal/roundintegration"
	"github.com/tobocop2/beehave/internal/roundsync"
	"github.com/tobocop2/beehave/internal/takeover"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The step kinds and keywords of the builtins that write the forge for a round.
const (
	roundSyncKind        = "round_sync"
	roundLandKind        = "round_land"
	roundCutKind         = "round_cut"
	roundIntegrationKind = "integration_pull"
	boardCardKind        = "board_card"
	prTakeKind           = "pr_take"

	trunkKeyword   = "trunk"
	prKeyword      = "pr"
	baseTipKeyword = "base_tip"

	// refusedRC is the exit code of a refusal of a round tool, which ends the item.
	refusedRC = 2
)

var errNoRounds = &absentError{message: "this build cannot write the forge for a round. Use a build that sets the round writer", step: "write the forge for a round"}

// TakeRequest is one take step: the pull request, the login that takes it, the idle time and the grace period, and the step.
type TakeRequest struct {
	Request takeover.Request
	Me      string
	Idle    time.Duration
	Grace   time.Duration
	Now     time.Time
}

// rounds runs the forge writes of the round tools as the writer role of the project. Each call acts on the forge of the project and returns what the tool printed as data.
// Every method needs the claim of the run, which runWrites checks before it calls.
type rounds interface {
	// Sync merges the trunk into a sync branch of the round branch and opens the sync pull request.
	Sync(ctx context.Context, project string, req roundsync.Request) (roundsync.Result, error)
	// Land merges the sync pull request pinned to the head that the gate ran on.
	Land(ctx context.Context, project string, req roundsync.LandRequest) (roundsync.Result, error)
	// Cut makes the next round branch and moves the open pull requests to it. A refusal is a Result with the stop Refused.
	Cut(ctx context.Context, project string, req roundcut.Request) (roundcut.Result, error)
	// Integrate finds or opens the integration pull request and writes its body.
	Integrate(ctx context.Context, project string, req roundintegration.Request) (roundintegration.Result, error)
	// Place puts the card on the project board, sets its column, and returns the column that the board shows afterwards.
	Place(ctx context.Context, project string, card model.Card) (shown string, err error)
	// Take runs the notice or the take of a pull request of somebody else. The run holds the claim, so the take reads and writes none.
	Take(ctx context.Context, project string, req TakeRequest) (takeover.Result, error)
}

// roundOps returns the round writer of the engine, or the error of a build that sets none.
func (s *script) roundOps() (rounds, error) { //nolint:ireturn // the build sets the round writer as its interface
	if s.e.deps.Rounds == nil {
		return nil, errNoRounds
	}

	return s.e.deps.Rounds, nil
}

// Sync writes through the claim of the run.
func (w runWrites) Sync(ctx context.Context, project string, req roundsync.Request) (result roundsync.Result, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		result, err = w.e.deps.Rounds.Sync(c, project, req)

		return err
	})

	return result, err
}

// Land writes through the claim of the run.
func (w runWrites) Land(ctx context.Context, project string, req roundsync.LandRequest) (result roundsync.Result, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		result, err = w.e.deps.Rounds.Land(c, project, req)

		return err
	})

	return result, err
}

// Cut writes through the claim of the run.
func (w runWrites) Cut(ctx context.Context, project string, req roundcut.Request) (result roundcut.Result, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		result, err = w.e.deps.Rounds.Cut(c, project, req)

		return err
	})

	return result, err
}

// Integrate writes through the claim of the run.
func (w runWrites) Integrate(ctx context.Context, project string, req roundintegration.Request) (result roundintegration.Result, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		result, err = w.e.deps.Rounds.Integrate(c, project, req)

		return err
	})

	return result, err
}

// Place writes through the claim of the run.
func (w runWrites) Place(ctx context.Context, project string, card model.Card) (shown string, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		shown, err = w.e.deps.Rounds.Place(c, project, card)

		return err
	})

	return shown, err
}

// Take writes through the claim of the run.
func (w runWrites) Take(ctx context.Context, project string, req TakeRequest) (result takeover.Result, err error) {
	err = claimedCall(ctx, w.trusted, func(c context.Context) error {
		result, err = w.e.deps.Rounds.Take(c, project, req)

		return err
	})

	return result, err
}

// syncOutputs are the fields of round_sync and round_land: the state first, then each field that the state sets, as the empty text or 0 when it does not.
func syncOutputs(r roundsync.Result) map[string]any {
	return map[string]any{
		stateField: string(r.State), branchKeyword: r.Branch, "source": r.Source, numberField: r.PR, headKeyword: r.Head, baseTipKeyword: r.BaseTip, "reason": r.Reason,
	}
}

// roundSync merges the trunk into the round branch through a sync branch and a draft pull request, and returns the state of the sync.
func (s *script) roundSync(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var req roundsync.Request

	var label string

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &req.Base, trunkKeyword: &req.Trunk, nameKeyword: &label})
	if err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	return s.durable(roundSyncKind, label, func(ctx context.Context, name string) (stepResult, error) {
		result, err := s.writes().Sync(ctx, s.in.Project, req)

		return s.ended(ctx, name, stepResult{Outputs: syncOutputs(result)}, err)
	})
}

// roundLand lands the sync pull request pinned to the gated head and the recorded base tip.
func (s *script) roundLand(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	req, label, err := landOf(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	return s.durable(roundLandKind, label, func(ctx context.Context, name string) (stepResult, error) {
		result, err := s.writes().Land(ctx, s.in.Project, req)

		return s.ended(ctx, name, stepResult{Outputs: syncOutputs(result)}, err)
	})
}

// landOf reads the landing that a round_land call names. The pull request must be a number above 0.
func landOf(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (req roundsync.LandRequest, label string, err error) {
	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{prKeyword: &req.PR, headKeyword: &req.Head, baseTipKeyword: &req.BaseTip, nameKeyword: &label})
	if err != nil {
		return req, label, err
	}

	return req, label, aboveZero(b.Name(), prKeyword, req.PR)
}

// roundCut cuts the next round branch. The step records a refusal of the cut as data, and the workflow decides what it does.
func (s *script) roundCut(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var req roundcut.Request

	var next, label string

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"old": &req.Cut.OldBase, "new": &next, prKeyword: &req.Cut.OldPR, trunkKeyword: &req.Cut.Trunk, nameKeyword: &label})
	if err != nil {
		return nil, err
	}

	if err = cutArguments(b.Name(), req.Cut.OldBase, next, req.Cut.OldPR); err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	req.Cut.NewBase, req.Cut.Roman = next, next[strings.LastIndex(next, "-")+1:]

	return s.durable(roundCutKind, label, s.cutWrite(req))
}

// aboveZero refuses a number that is not above 0. The error names the builtin and the keyword.
func aboveZero(builtin, keyword string, number int) error {
	if number <= 0 {
		return fmt.Errorf("%s: %s must be a number above 0, got %d", builtin, keyword, number)
	}

	return nil
}

// cutArguments refuses a cut that names no old branch, a new branch with no Roman numeral after its last dash, or no pull request.
func cutArguments(builtin, old, next string, pr int) error {
	switch {
	case old == "":
		return fmt.Errorf("%s: old must name the old round branch, got an empty text", builtin)
	case next[strings.LastIndex(next, "-")+1:] == "":
		return fmt.Errorf("%s: new must end in a Roman numeral after a dash, such as round-ii, got %q", builtin, next)
	}

	return aboveZero(builtin, prKeyword, pr)
}

// cutWrite is the recorded write of a cut.
func (s *script) cutWrite(req roundcut.Request) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, name string) (stepResult, error) {
		result, err := s.writes().Cut(ctx, s.in.Project, req)
		recorded := stepResult{Outputs: map[string]any{"lines": nonNil(result.Lines)}}

		if result.Stop == roundcut.Refused {
			recorded.Stopped = refusedStop(name, result.Lines)
		}

		return s.ended(ctx, name, recorded, err)
	}
}

// ended turns the error of a forge write into the end of the item, with the error as the lines of the step, as a tool that exits on code 2 does.
// The end of the claim or of the run is no answer of the forge: it stays an error and fails the run.
func (s *script) ended(ctx context.Context, name string, recorded stepResult, err error) (stepResult, error) {
	if err == nil {
		return recorded, nil
	}

	if errors.Is(err, errClaimLost) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return recorded, err
	}

	recorded.Stopped = refusedStop(name, []string{s.e.redact(err.Error())})

	return recorded, nil
}

// refusedStop is the stop of a step that the tool refused: it ends the item with the lines of the refusal, as a command that ends on exit code 2 does.
func refusedStop(name string, lines []string) *Stopped {
	return &Stopped{Step: name, RC: refusedRC, Output: strings.Join(lines, "\n")}
}

// nonNil returns lines, or an empty list for none, so the recorded field is a list and never null.
func nonNil(lines []string) []string {
	if lines == nil {
		return []string{}
	}

	return lines
}

// roundIntegration finds or opens the integration pull request of the round and writes its body from the pull requests merged into the round branch.
func (s *script) roundIntegration(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var req roundintegration.Request

	var label string

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &req.Base, "into": &req.Into, prKeyword: &req.PR, nameKeyword: &label})
	if err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	return s.durable(roundIntegrationKind, label, func(ctx context.Context, name string) (stepResult, error) {
		result, err := s.writes().Integrate(ctx, s.in.Project, req)

		return s.ended(ctx, name, stepResult{Outputs: map[string]any{numberField: result.PR, stateField: result.State}}, err)
	})
}

// errCardArguments says that a board_card call names the card by exactly one of pr and issue.
var errCardArguments = errors.New("board_card takes exactly one of pr and issue, as a number above 0")

// errCardStatus says that a board_card call names no column.
var errCardStatus = errors.New("board_card takes a status that is not blank")

// boardCard puts the pull request or the issue on the project board, sets its column and reads the card back. A board that shows another column is an error.
func (s *script) boardCard(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	card, label, err := cardOf(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	return s.durable(boardCardKind, label, s.cardWrite(card))
}

// cardOf reads the card that a board_card call names.
func cardOf(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (card model.Card, label string, err error) {
	var pr, issue int

	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{"board": &card.Board, statusKeyword: &card.Status, "owner": &card.Owner, prKeyword: &pr, "issue": &issue, nameKeyword: &label})
	if err != nil {
		return card, label, err
	}

	if (pr > 0) == (issue > 0) || card.Board <= 0 {
		return card, label, errCardArguments
	}

	if strings.TrimSpace(card.Status) == "" {
		return card, label, errCardStatus
	}

	card.Number, card.Pull = max(pr, issue), pr > 0

	return card, label, nil
}

// cardWrite is the recorded write of a card: it places the card and ends the item when the board shows another column.
func (s *script) cardWrite(card model.Card) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, name string) (stepResult, error) {
		shown, err := s.writes().Place(ctx, s.in.Project, card)
		recorded := stepResult{Outputs: map[string]any{statusKeyword: shown}}

		if err == nil && shown != card.Status {
			recorded.Stopped = refusedStop(name, []string{fmt.Sprintf("the board %d shows %q for card %d after the write; wanted %q", card.Board, shown, card.Number, card.Status)})
		}

		return s.ended(ctx, name, recorded, err)
	}
}

// prTake posts the notice for a pull request of somebody else, or takes it after the grace period. The result holds the code of the step and the lines that it showed.
func (s *script) prTake(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	req, label, err := s.takeOf(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if _, err = s.roundOps(); err != nil {
		return nil, err
	}

	return s.durable(prTakeKind, label, s.takeWrite(req))
}

// takeOf reads the take that a pr_take call names.
func (s *script) takeOf(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (req TakeRequest, label string, err error) {
	var idle, grace, step string

	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		prKeyword: &req.Request.PR, "me": &req.Me, "idle": &idle, "grace": &grace, "step": &step, nameKeyword: &label,
	})
	if err != nil {
		return req, label, err
	}

	if err = takeArguments(b.Name(), req.Request.PR, step); err != nil {
		return req, label, err
	}

	if req.Idle, req.Grace, err = hoursOf(idle, grace); err != nil {
		return req, label, err
	}

	req.Request.Mode, req.Now = takeover.Mode(step), s.e.deps.Clock.reading()

	return req, label, nil
}

// takeArguments refuses a take with no pull request above 0 and a step that is not one of the steps of pr_take.
func takeArguments(builtin string, pr int, step string) error {
	if !slices.Contains(workflow.TakeSteps(), step) {
		return fmt.Errorf("%s: step must be one of %s, got %q", builtin, strings.Join(workflow.TakeSteps(), ", "), step)
	}

	return aboveZero(builtin, prKeyword, pr)
}

// takeWrite is the recorded write of a take: a refusal of a rule or a guard ends the item with the lines of the step.
func (s *script) takeWrite(req TakeRequest) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, name string) (stepResult, error) {
		result, err := s.writes().Take(ctx, s.in.Project, req)
		recorded := stepResult{Outputs: map[string]any{"code": result.Code, "lines": nonNil(result.Lines)}}

		if result.Code != 0 {
			recorded.Stopped = refusedStop(name, result.Lines)
		}

		return s.ended(ctx, name, recorded, err)
	}
}

// hoursOf reads the idle time and the grace period, each a number of hours.
func hoursOf(idle, grace string) (idleTime, graceTime time.Duration, err error) {
	idleTime, err = hours("idle", idle)
	if err != nil {
		return 0, 0, err
	}

	graceTime, err = hours("grace", grace)

	return idleTime, graceTime, err
}

// hours reads a number of hours of one keyword. A value that is not a number of hours above 0 is an error.
func hours(keyword, text string) (time.Duration, error) {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a number of hours, such as 24: got %q", keyword, text)
	}

	return time.Duration(value * float64(time.Hour)), nil
}

// roundDefs lists the builtins that write the forge for a round. The engine refuses a call of one of them in a project with no repository.
func roundDefs() []builtinDef {
	return []builtinDef{
		{name: boardCardKind, call: (*script).boardCard, repository: true},
		{name: prTakeKind, call: (*script).prTake, repository: true},
		{name: roundCutKind, call: (*script).roundCut, repository: true},
		{name: roundIntegrationKind, call: (*script).roundIntegration, repository: true},
		{name: roundLandKind, call: (*script).roundLand, repository: true},
		{name: roundSyncKind, call: (*script).roundSync, repository: true},
	}
}
