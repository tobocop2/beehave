// Package roundcut cuts the next remediation round after the integration pull request merges into the trunk.
// It moves every open pull request on the old base to a new base at the trunk tip. It never deletes the old branch.
package roundcut

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// listLimit is the most pull requests one list reads.
const listLimit = 1000

// Stop is how a round cut ended.
type Stop int

// The stops of a round cut.
const (
	Done Stop = iota
	Refused
)

// Forge is the forge the round cut works on. It has no call that deletes a branch.
type Forge interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	CreateBranch(ctx context.Context, branch, sha string) error
	SetBase(ctx context.Context, pr int, base string) error
}

// Cut holds the names of one round cut. Roman is the number of the new round.
type Cut struct {
	Roman   string
	OldPR   int
	OldBase string
	NewBase string
	Trunk   string
}

// Request is one round cut. A dry run takes the same path and says what each write would do.
type Request struct {
	Cut Cut
	Dry bool
}

// Result is how the cut ended and the lines to show.
type Result struct {
	Stop  Stop
	Lines []string
}

// cutter holds the state of one run.
type cutter struct {
	ctx   context.Context
	forge Forge
	req   Request
	lines []string
}

// Run cuts the round. A refusal comes back as a Result with the stop Refused, never as an error.
func Run(ctx context.Context, forge Forge, req Request) Result {
	c := &cutter{ctx: ctx, forge: forge, req: req, lines: []string{}}

	return Result{Stop: c.run(), Lines: c.lines}
}

func (c *cutter) say(lines ...string) {
	c.lines = append(c.lines, lines...)
}

func (c *cutter) refuse(lines ...string) Stop {
	c.say(lines...)

	return Refused
}

func (c *cutter) run() Stop {
	if stop := c.checkMerged(); stop != Done {
		return stop
	}

	tip, stop := c.readTip()
	if stop != Done {
		return stop
	}

	if stop := c.moveRows(tip); stop != Done {
		return stop
	}

	return c.finish()
}

// finish checks that nothing is left on the old base (a dry run changed nothing, so it skips this) and shows the notes.
func (c *cutter) finish() Stop {
	if !c.req.Dry {
		if stop := c.checkLeft(); stop != Done {
			return stop
		}
	}

	c.say(c.notes()...)

	return Done
}

// moveRows lists the pull requests on the old base, makes or resumes the new base and moves each row to it.
func (c *cutter) moveRows(tip string) Stop {
	rows, err := c.list("number,title")
	if err != nil {
		return c.refuse(err.Error())
	}

	c.say(fmt.Sprintf("open PRs to retarget: %d", len(rows)))

	for _, row := range rows {
		c.say(fmt.Sprintf("#%d %s", row.Number, flat(row.Title)))
	}

	if stop := c.ensureNewBase(tip); stop != Done {
		return stop
	}

	return c.retarget(rows)
}

func (c *cutter) readTip() (string, Stop) {
	trunk := c.req.Cut.Trunk

	tip, found, err := c.forge.BranchRef(c.ctx, trunk)
	if err != nil || !found {
		return "", c.refuse("REFUSE: cannot read the tip of " + trunk)
	}

	c.say(trunk + " tip: " + tip)

	return tip, Done
}

func (c *cutter) retarget(rows []model.PullRequest) Stop {
	for _, row := range rows {
		if row.Number == 0 {
			continue
		}

		if stop := c.setBase(row.Number); stop != Done {
			return stop
		}
	}

	return Done
}

// flat turns a tab or a newline in a title into a space, so a title stays on its row.
func flat(title string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(title)
}

func (c *cutter) list(fields string) ([]model.PullRequest, error) {
	return c.forge.PRList(c.ctx, listLimit, "--state", "open", "--base", c.req.Cut.OldBase, "--json", fields)
}

func (c *cutter) checkMerged() Stop {
	cut := c.req.Cut

	pr, err := c.forge.PRView(c.ctx, cut.OldPR, "state", "mergedAt", "baseRefName")
	if err != nil {
		return c.refuse(fmt.Sprintf("REFUSE: cannot read integration pull request #%d", cut.OldPR))
	}

	state := or(pr.State)
	c.say(fmt.Sprintf("integration PR #%d: %s %s %s", cut.OldPR, state, or(pr.MergedAt), or(pr.BaseRefName)))

	if state != "MERGED" {
		return c.refuse(fmt.Sprintf("REFUSE: #%d is not merged. Cut the round only after it lands.", cut.OldPR))
	}

	return Done
}

// or is the value of a field the forge may leave out, or a dash.
func or(field *string) string {
	if field == nil {
		return "-"
	}

	return *field
}

func (c *cutter) ensureNewBase(tip string) Stop {
	cut := c.req.Cut

	found, exists, err := c.forge.BranchRef(c.ctx, cut.NewBase)
	if err != nil {
		return c.refuse("REFUSE: cannot read " + cut.NewBase)
	}

	if !exists {
		return c.createBase(tip)
	}

	if found != tip {
		return c.refuseMoved(found, tip)
	}

	c.say(fmt.Sprintf("%s already exists at the %s tip: resuming the retarget", cut.NewBase, cut.Trunk))

	return Done
}

// refuseMoved refuses a new base that is not at the trunk tip, and says how to move it.
func (c *cutter) refuseMoved(found, tip string) Stop {
	cut := c.req.Cut

	return c.refuse(
		fmt.Sprintf("REFUSE: %s already exists at %s, not at the %s tip %s.", cut.NewBase, found, cut.Trunk, tip),
		fmt.Sprintf("        If %s moved since the first run and nobody pushed to %s, move it to %s in the forge and run again.",
			cut.Trunk, cut.NewBase, tip),
	)
}

func (c *cutter) createBase(tip string) Stop {
	base := c.req.Cut.NewBase

	if c.req.Dry {
		c.say(fmt.Sprintf("WOULD: create branch %s at %s", base, tip))

		return Done
	}

	if err := c.forge.CreateBranch(c.ctx, base, tip); err != nil {
		return c.refuse(fmt.Sprintf("REFUSE: cannot create branch %s: %v", base, err))
	}

	return Done
}

func (c *cutter) setBase(number int) Stop {
	base := c.req.Cut.NewBase

	if c.req.Dry {
		c.say(fmt.Sprintf("WOULD: set the base of #%d to %s", number, base))

		return Done
	}

	if err := c.forge.SetBase(c.ctx, number, base); err != nil {
		return c.refuse(fmt.Sprintf("REFUSE: cannot set the base of #%d to %s: %v", number, base, err))
	}

	return Done
}

func (c *cutter) checkLeft() Stop {
	old := c.req.Cut.OldBase

	rows, err := c.list("number")
	if err != nil {
		return c.refuse(err.Error())
	}

	c.say(fmt.Sprintf("still on %s after retarget: %d", old, len(rows)))

	if len(rows) > 0 {
		return c.refuse(fmt.Sprintf("REFUSE to delete %s: %d PRs still target it.", old, len(rows)))
	}

	c.say(fmt.Sprintf("SAFE to delete %s. Delete it by hand after confirming nothing is stacked on it.", old))

	return Done
}

func (c *cutter) notes() []string {
	cut := c.req.Cut

	return []string{
		"",
		"Remaining steps, deliberately not automated:",
		fmt.Sprintf("  1. Open the round %s integration pull request (%s -> %s) AFTER the first fix merges;",
			cut.Roman, cut.NewBase, cut.Trunk),
		"     GitHub refuses a pull request with no diff.",
		fmt.Sprintf("  2. Point the integration step at the new pull request and at %s,", cut.NewBase),
		"     and update its defaults.",
		"  3. Update the lane rules, the queue and the profile with the new base and its tip.",
		"  4. Tell every live lane the new base SHA; a lane gating against the old tip produces a stale union.",
	}
}
