package remedy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/ledger"
)

const ledgerSyntax = "ledger add <key> <lane> <kind> --state <folder> [--mechanism <token>] [--model <token>] [--round <n>] [--words <text>] [--body-file <path> | --pr <number>] | ledger count <key> <kinds> --state <folder> | ledger rounds <key> <mechanism> --state <folder> | ledger prior <key> <lane> --state <folder> | ledger closes <key> --state <folder>"

const verbAdd = "add"

// The count of arguments that a ledger verb takes before its flags.
const (
	keyArgs  = 1
	pairArgs = 2
	laneArgs = 3
)

// ledgerVerb is one verb of the ledger command: its name, the count of arguments it takes before its flags, and its read.
// The read of add is nil, because add writes.
type ledgerVerb struct {
	name string
	args int
	read func(book Book, pos []string) ([]string, error)
}

// findLedgerVerb gives the verb of a name, and false for a name the command does not know. The parser takes a verb from here, so no verb can reach the run without its own read.
func findLedgerVerb(name string) (ledgerVerb, bool) {
	switch name {
	case verbAdd:
		return ledgerVerb{name: verbAdd, args: laneArgs}, true
	case "count":
		return ledgerVerb{name: "count", args: pairArgs, read: readCount}, true
	case "rounds":
		return ledgerVerb{name: "rounds", args: pairArgs, read: readRounds}, true
	case "prior":
		return ledgerVerb{name: "prior", args: pairArgs, read: readPrior}, true
	case "closes":
		return ledgerVerb{name: "closes", args: keyArgs, read: readCloses}, true
	default:
		return ledgerVerb{}, false
	}
}

func readCount(book Book, pos []string) ([]string, error) {
	count, err := book.Count(pos[0], pos[1])

	return []string{strconv.Itoa(count)}, err
}

func readRounds(book Book, pos []string) ([]string, error) {
	rounds, err := book.Rounds(pos[0], pos[1])

	return []string{fmt.Sprintf("%d open=%t", rounds.Count, rounds.Open)}, err
}

func readPrior(book Book, pos []string) ([]string, error) {
	prior, err := book.Prior(pos[0], pos[1])

	return []string{strconv.Itoa(prior)}, err
}

func readCloses(book Book, pos []string) ([]string, error) {
	return book.Closes(pos[0])
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
func parseLedger(args []string, errOut io.Writer) (ledgerVerb, []string, ledgerFlags, bool) {
	if len(args) == 0 {
		return ledgerVerb{}, nil, ledgerFlags{}, ledgerUsage(errOut)
	}

	verb, known := findLedgerVerb(args[0])
	if !known {
		say(errOut, "remedy ledger: '%s' is not a ledger verb.\n", args[0])

		return ledgerVerb{}, nil, ledgerFlags{}, ledgerUsage(errOut)
	}

	if len(args) <= verb.args {
		return ledgerVerb{}, nil, ledgerFlags{}, ledgerUsage(errOut)
	}

	given, ok := readLedgerFlags(verb, args[1+verb.args:], errOut)

	return verb, args[1 : 1+verb.args], given, ok
}

// readLedgerFlags reads the flags that follow the arguments of a verb. It gives false when a flag is bad or the state folder is missing.
func readLedgerFlags(verb ledgerVerb, args []string, errOut io.Writer) (ledgerFlags, bool) {
	var given ledgerFlags

	flags := flag.NewFlagSet("remedy ledger "+verb.name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	given.declare(flags, verb.name == verbAdd)

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
// Only add prints lines together with an error, because only add can write its row and still fail, so a row that is written but not released still shows its issues.
// A read verb prints no line when it fails.
func ledgerCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	verb, pos, given, ok := parseLedger(args, s.err)
	if !ok {
		return ExitRefused
	}

	lines, err := ledgerRun(ctx, deps, verb, pos, given)

	for _, line := range lines {
		say(s.out, "%s\n", line)
	}

	if err != nil {
		say(s.err, "remedy ledger %s: %v\n", verb.name, err)

		return ExitRefused
	}

	return ExitPass
}

// ledgerRun runs one verb on the ledger of the state folder and gives the lines to print. The verb is one that parseLedger took from findLedgerVerb.
// Only add returns its lines straight, with its error. Every other verb is a read and goes through ledgerRead.
func ledgerRun(ctx context.Context, deps Deps, verb ledgerVerb, pos []string, given ledgerFlags) ([]string, error) {
	book := deps.Book(given.state)

	if verb.read == nil {
		return ledgerAdd(ctx, deps, book, pos, given)
	}

	return ledgerRead(book, verb, pos)
}

// ledgerRead runs a read verb and gives no line when the read failed, so a failed read prints no value.
// A read verb that is new gets this rule because it can be reached only through here.
func ledgerRead(book Book, verb ledgerVerb, pos []string) ([]string, error) {
	lines, err := verb.read(book, pos)
	if err != nil {
		return nil, err
	}

	return lines, nil
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

// pullClosing reads the issues a pull request of the origin repository closes.
func pullClosing(ctx context.Context, deps Deps, pr int) ([]string, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return nil, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return nil, err
	}

	numbers, err := closingIssues(ctx, forge, pr)

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
