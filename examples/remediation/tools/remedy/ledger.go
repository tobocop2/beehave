package remedy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
	"github.com/tobocop2/obee/examples/remediation/tools/scope"
)

const ledgerSyntax = "ledger add <key> <lane> <kind> --state <folder> [--mechanism <token>] [--model <token>] [--round <n>] [--words <text>] [--body-file <path> | --pr <number>] | ledger count <key> <kinds> --state <folder> | ledger rounds <key> <mechanism> --state <folder> | ledger prior <key> <lane> --state <folder> | ledger closes <key> --state <folder>"

const verbAdd = "add"

// The count of arguments that a ledger verb takes before its flags.
const (
	keyArgs  = 1
	pairArgs = 2
	laneArgs = 3
)

// ledgerArgs is the count of arguments that a verb takes before its flags. A verb that is not known gives false.
func ledgerArgs(verb string) (int, bool) {
	switch verb {
	case verbAdd:
		return laneArgs, true
	case "count", "rounds", "prior":
		return pairArgs, true
	case "closes":
		return keyArgs, true
	}

	return 0, false
}

// ledgerFlags are the values of the flags of the ledger verbs. Only add reads the flags after --state.
type ledgerFlags struct {
	state, mechanism, model, words, bodyFile string
	round, pr                                int
}

func (f *ledgerFlags) declare(flags *flag.FlagSet, add bool) {
	flags.StringVar(&f.state, "state", "", "the state folder that holds the ledger")

	if !add {
		return
	}

	flags.StringVar(&f.mechanism, "mechanism", "", "the mechanism of a fix lane")
	flags.StringVar(&f.model, "model", "", "the model of a fix lane")
	flags.IntVar(&f.round, "round", 0, "the round of a fix lane")
	flags.StringVar(&f.words, "words", "", "the words of the owner, for an owner-add row")
	flags.StringVar(&f.bodyFile, "body-file", "", "a file that holds the pull request body, for the first review of a key")
	flags.IntVar(&f.pr, "pr", 0, "the pull request to read the body from, for the first review of a key")
}

// parseLedger reads the verb, its arguments and the flags. It shows the usage and gives false for a bad argument.
func parseLedger(args []string, errOut io.Writer) (string, []string, ledgerFlags, bool) {
	if len(args) == 0 {
		return "", nil, ledgerFlags{}, ledgerUsage(errOut)
	}

	verb := args[0]

	count, known := ledgerArgs(verb)
	if !known || len(args) < 1+count {
		return "", nil, ledgerFlags{}, ledgerUsage(errOut)
	}

	given, ok := readLedgerFlags(verb, args[1+count:], errOut)

	return verb, args[1 : 1+count], given, ok
}

// readLedgerFlags reads the flags that follow the arguments of a verb. It gives false when a flag is bad or the state folder is missing.
func readLedgerFlags(verb string, args []string, errOut io.Writer) (ledgerFlags, bool) {
	var given ledgerFlags

	flags := flag.NewFlagSet("remedy ledger "+verb, flag.ContinueOnError)
	flags.SetOutput(errOut)
	given.declare(flags, verb == verbAdd)

	if flags.Parse(args) != nil {
		return given, false
	}

	if given.state == "" || flags.NArg() > 0 {
		return given, ledgerUsage(errOut)
	}

	return given, true
}

func ledgerUsage(errOut io.Writer) bool {
	say(errOut, "usage: remedy %s\n", ledgerSyntax)

	return false
}

// ledgerCommand reads or writes the lane ledger in the state folder that --state names. A read that fails and a row that the ledger refuses are exit code 2.
func ledgerCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	verb, pos, given, ok := parseLedger(args, s.err)
	if !ok {
		return ExitRefused
	}

	lines, err := ledgerRun(ctx, deps, verb, pos, given)
	if err != nil {
		say(s.err, "remedy ledger %s: %v\n", verb, err)

		return ExitRefused
	}

	for _, line := range lines {
		say(s.out, "%s\n", line)
	}

	return ExitPass
}

// ledgerRun runs one verb on the ledger of the state folder and gives the lines to print. The verb is one that parseLedger checked.
func ledgerRun(ctx context.Context, deps Deps, verb string, pos []string, given ledgerFlags) ([]string, error) {
	book := deps.Book(given.state)

	switch verb {
	case verbAdd:
		return ledgerAdd(ctx, deps, book, pos, given)
	case "count":
		count, err := book.Count(pos[0], pos[1])

		return []string{strconv.Itoa(count)}, err
	case "rounds":
		rounds, err := book.Rounds(pos[0], pos[1])

		return []string{fmt.Sprintf("%d open=%t", rounds.Count, rounds.Open)}, err
	case "prior":
		prior, err := book.Prior(pos[0], pos[1])

		return []string{strconv.Itoa(prior)}, err
	}

	return book.Closes(pos[0])
}

// ledgerAdd records one lane and gives the issues that the first review of the key recorded.
func ledgerAdd(ctx context.Context, deps Deps, book Book, pos []string, given ledgerFlags) ([]string, error) {
	kind, err := ledger.ParseKind(pos[2])
	if err != nil {
		return nil, err
	}

	closing, err := given.closingReader(ctx, deps)
	if err != nil {
		return nil, err
	}

	added, err := book.Add(ledger.Request{
		Key: pos[0], Lane: pos[1], Kind: kind, Mechanism: given.mechanism, Model: given.model, Round: given.round,
		Words: given.words, At: deps.Now(), Closing: closing,
	})

	return added.Closes, err
}

// closingReader gives the read of the issues the pull request closes: the closing keywords of the file of --body-file, else the read of the pull request of --pr, else none.
// The pull request is read only when the ledger asks for it, so a row that needs no issues opens no forge.
func (f ledgerFlags) closingReader(ctx context.Context, deps Deps) (read func() ([]string, error), err error) {
	switch {
	case f.bodyFile != "" && f.pr != 0:
		err = errors.New("give --body-file or --pr, not both")
	case f.bodyFile != "":
		read = func() ([]string, error) {
			text, err := brief.ReadText(f.bodyFile)

			return ledger.ClosingNumbers(text), err
		}
	case f.pr != 0:
		read = func() ([]string, error) { return pullClosing(ctx, deps, f.pr) }
	}

	return read, err
}

// pullClosing reads the issues a pull request of the origin repository closes, with the read that the scope check uses.
func pullClosing(ctx context.Context, deps Deps, pr int) ([]string, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return nil, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return nil, err
	}

	numbers, err := scope.ReaderOf(forge).ClosingIssues(ctx, pr)

	return texts(numbers), err
}

// texts spells issue numbers in numeric order.
func texts(numbers []int) []string {
	spelled := make([]string, 0, len(numbers))
	for _, number := range slices.Sorted(slices.Values(numbers)) {
		spelled = append(spelled, strconv.Itoa(number))
	}

	return spelled
}
