package remedy

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/tobocop2/beehave/examples/remediation/tools/takeover"
)

const takeSyntax = "take <pr> --me <login> --idle <hours> --grace <hours> [--head <sha>] [--item <id>] [--apply | --take [--reply-read]]"

// noHours marks an hours flag that was not given. A real value is never negative.
const noHours = -1.0

// readOwner is the owner that the claim read asks as. The engine gives each run a random owner token and no login maps to an owner, so the read uses one fixed owner that never holds a claim.
// Any live claim on the item is then another owner's.
const readOwner = "remedy"

// takeCommand decides whether to take a pull request. Every refusal of a rule, a guard or an argument is exit code 2, and so is a failed read.
func takeCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseTake(args, s.err)
	if !ok {
		return ExitRefused
	}

	given.c.Now = deps.Now()

	result, err := takeRun(ctx, deps, given)
	if err != nil {
		say(s.err, "remedy take: %v\n", err)

		return ExitRefused
	}

	return showTake(s, result)
}

// showTake prints the lines of the step and gives its exit code.
func showTake(s streams, result takeover.Result) int {
	for _, line := range result.Lines {
		say(s.out, "%s\n", line)
	}

	if result.Code != 0 {
		return ExitRefused
	}

	return ExitPass
}

// takeRun opens the forge of the origin remote and runs the step on it.
// The claim store opens only when the step reads the claim, after every guard and rule that needs no store.
func takeRun(ctx context.Context, deps Deps, given taking) (takeover.Result, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return takeover.Result{}, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return takeover.Result{}, err
	}

	given.c.Free = freeCall(deps, repo, given.item)

	return takeover.Run(ctx, forge, given.req, given.c)
}

// freeCall gives the claim read of the takeover: it opens the claim store and refuses when any owner holds the tracker item.
// It writes no claim.
func freeCall(deps Deps, repo, item string) func(context.Context) error {
	return func(ctx context.Context) error {
		store, err := deps.Claims(repo)
		if err != nil {
			return fmt.Errorf("cannot open the claim store: %w", err)
		}

		return store.EnsureFree(ctx, readOwner, item)
	}
}

// takeFlags are the values of the flags of take.
type takeFlags struct {
	me          string
	head, item  string
	idle, grace float64
	apply, take bool
	replyRead   bool
}

func (f *takeFlags) declare(flags *flag.FlagSet) {
	// The login is a flag so the tool keeps no state. The round settings that hold the login leave the engine in a later change.
	flags.StringVar(&f.me, "me", "", "the login that takes the pull request")
	flags.StringVar(&f.item, "item", "", "the tracker item that the run claims; --apply and --take read its claim")
	flags.StringVar(&f.head, "head", "", "the head commit the caller works on; the step refuses a pull request that moved from it")
	flags.Float64Var(&f.idle, "idle", noHours, "hours without activity before an unassigned pull request may be taken")
	flags.Float64Var(&f.grace, "grace", noHours, "hours between the notice and the take")
	flags.BoolVar(&f.apply, "apply", false, "post the notice")
	flags.BoolVar(&f.take, "take", false, "take the pull request after the grace period")
	flags.BoolVar(&f.replyRead, "reply-read", false, "say that the replies after the notice were read")
}

// step is the step that the flags ask for. Both flags together ask for none.
func (f *takeFlags) step() (takeover.Mode, bool) {
	switch {
	case f.apply && f.take:
		return takeover.ModeDry, false
	case f.apply:
		return takeover.ModeApply, true
	case f.take:
		return takeover.ModeTake, true
	}

	return takeover.ModeDry, true
}

// context is the settings of the round that the flags give. The caller adds the time.
func (f *takeFlags) context() takeover.Context {
	return takeover.Context{
		Me:    f.me,
		Idle:  hoursOf(f.idle),
		Grace: hoursOf(f.grace),
	}
}

func hoursOf(hours float64) time.Duration { return time.Duration(hours * float64(time.Hour)) }

// taking is what the arguments of take ask for: the request, the settings of the round, and the tracker item that the claim read checks.
type taking struct {
	req  takeover.Request
	c    takeover.Context
	item string
}

// parseTake reads the arguments of take: the number first, then the flags. It shows the usage and gives false for a bad argument.
func parseTake(args []string, errOut io.Writer) (taking, bool) {
	if len(args) == 0 {
		return takeRefused(errOut)
	}

	number, err := strconv.Atoi(args[0])
	if err != nil || number <= 0 {
		return takeRefused(errOut)
	}

	given, extra, ok := readTakeFlags(args[1:], errOut)
	if !ok {
		return taking{}, false
	}

	return given.taking(number, extra, errOut)
}

// taking checks the flags as a whole and gives what they ask for.
func (f *takeFlags) taking(number int, extra bool, errOut io.Writer) (taking, bool) {
	mode, ok := f.step()
	if !ok || extra || !f.complete() {
		return takeRefused(errOut)
	}

	if mode != takeover.ModeDry && f.item == "" {
		return needItem(errOut)
	}

	req := takeover.Request{PR: number, Mode: mode, ReplyRead: f.replyRead, HeadWant: f.head}

	return taking{req: req, c: f.context(), item: f.item}, true
}

// readTakeFlags reads the flags that follow the number. It says if arguments are left over, and gives false when a flag is bad.
func readTakeFlags(args []string, errOut io.Writer) (takeFlags, bool, bool) {
	flags := flag.NewFlagSet("remedy take", flag.ContinueOnError)
	flags.SetOutput(errOut)

	var given takeFlags

	given.declare(flags)

	if flags.Parse(args) != nil {
		return takeFlags{}, false, false
	}

	return given, flags.NArg() > 0, true
}

// complete says that the login and both hours are given.
func (f *takeFlags) complete() bool { return f.me != "" && f.idle >= 0 && f.grace >= 0 }

// needItem says that a step which reads the claim needs the item, and gives false.
func needItem(errOut io.Writer) (taking, bool) {
	say(errOut, "remedy take: a step that reads the claim needs --item <id>, the tracker item that the run claims\n")

	return taking{}, false
}

// takeRefused shows the usage and gives false.
func takeRefused(errOut io.Writer) (taking, bool) {
	say(errOut, "usage: remedy %s\n", takeSyntax)

	return taking{}, false
}
