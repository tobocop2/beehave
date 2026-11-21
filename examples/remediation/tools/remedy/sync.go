package remedy

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/tobocop2/beehave/examples/remediation/tools/roundsync"
)

const (
	syncCheckUsage = "sync --base <branch> [--trunk <branch>] [--label <name>]"
	syncLandUsage  = "sync land --pr <number> --head <sha> --base-tip <sha>"
	syncUsage      = "usage: remedy sync --base <branch> [--trunk <branch>] [--label <name>]\n       remedy sync land --pr <number> --head <sha> --base-tip <sha>\n"
	landWord       = "land"
)

// errNoSync says that the forge of the repository cannot merge a branch into another.
var errNoSync = errors.New("the forge of this repository cannot sync a branch")

// syncCall is one call of the sync on a forge.
type syncCall func(context.Context, roundsync.Forge) (roundsync.Result, error)

// syncCommands are the subcommands that keep the round branch current with the trunk.
func syncCommands() []command {
	return []command{
		{
			name:    "sync",
			usage:   syncCheckUsage + " | " + syncLandUsage,
			summary: "merge the trunk into the round branch when the trunk moved, or land the sync pull request after its gate",
			run:     syncCommand,
		},
	}
}

// syncCommand checks whether the trunk moved and merges it into the round branch, or lands a sync that the gate passed.
// The one line of output names the state. A stale base, a moved head, a bad argument and a failed forge call are exit code 2.
func syncCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	land := len(args) > 0 && args[0] == landWord
	if land {
		args = args[1:]
	}

	call, ok := parseSync(land, args, s.err)
	if !ok {
		return ExitRefused
	}

	return showSync(ctx, deps, s, call)
}

// showSync runs the call, prints its line and gives its exit code.
func showSync(ctx context.Context, deps Deps, s streams, call syncCall) int {
	result, err := runSync(ctx, deps, call)
	if err != nil {
		say(s.err, "remedy sync: %v\n", err)

		return ExitRefused
	}

	say(s.out, "%s\n", result.Line())

	if result.Stops() {
		return ExitRefused
	}

	return ExitPass
}

// runSync opens the forge of the origin remote and runs the call on it.
func runSync(ctx context.Context, deps Deps, call syncCall) (roundsync.Result, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return roundsync.Result{}, err
	}

	opened, err := deps.Forge(repo)
	if err != nil {
		return roundsync.Result{}, err
	}

	syncer, ok := opened.(roundsync.Forge)
	if !ok {
		return roundsync.Result{}, errNoSync
	}

	return call(ctx, syncer)
}

// syncFlags are the values of the flags of sync and of sync land.
type syncFlags struct {
	base, trunk, head, baseTip, label string
	pr                                int
}

func (f *syncFlags) declare(flags *flag.FlagSet, land bool) {
	if land {
		flags.IntVar(&f.pr, "pr", 0, "the number of the pull request of the sync")
		flags.StringVar(&f.head, "head", "", "the head that the gate ran on")
		flags.StringVar(&f.baseTip, "base-tip", "", "the round base tip that the sync recorded")

		return
	}

	flags.StringVar(&f.base, "base", "", "the round branch")
	flags.StringVar(&f.trunk, "trunk", defaultTrunk, "the trunk branch")
	flags.StringVar(&f.label, "label", defaultLabel, "the label of the sync pull request; empty adds none")
}

// complete says whether every flag of the call has a value.
func (f *syncFlags) complete(land bool) bool {
	if land {
		return f.pr > 0 && f.head != "" && f.baseTip != ""
	}

	return f.base != "" && f.trunk != ""
}

// call is the call that the flags ask for.
func (f *syncFlags) call(land bool) syncCall {
	if land {
		return func(ctx context.Context, forge roundsync.Forge) (roundsync.Result, error) {
			return roundsync.Land(ctx, forge, roundsync.LandRequest{PR: f.pr, Head: f.head, BaseTip: f.baseTip})
		}
	}

	return func(ctx context.Context, forge roundsync.Forge) (roundsync.Result, error) {
		return roundsync.Check(ctx, forge, roundsync.Request{Base: f.base, Trunk: f.trunk, Label: f.label})
	}
}

// parseSync reads the arguments of sync or of sync land and gives the call they ask for. It shows the usage and gives false for a bad argument.
func parseSync(land bool, args []string, errOut io.Writer) (syncCall, bool) {
	flags := flag.NewFlagSet("remedy sync", flag.ContinueOnError)
	flags.SetOutput(errOut)

	var given syncFlags

	given.declare(flags, land)

	if flags.Parse(args) != nil {
		return nil, false
	}

	if !given.complete(land) || flags.NArg() > 0 {
		say(errOut, syncUsage)

		return nil, false
	}

	return given.call(land), true
}
