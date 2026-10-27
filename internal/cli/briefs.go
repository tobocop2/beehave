package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tobocop2/obee/internal/brief"
	"github.com/tobocop2/obee/internal/check/papers"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/ledger"
)

// fileMode is the mode of a file the commands write.
const fileMode = 0o600

func briefSpecs() []spec {
	return []spec{
		{use: "new <kind> <name> <model>", short: "Write the brief of a lane and add its ledger row", args: cobra.ExactArgs(threeArgs), bind: bindBriefNew},
		{use: "check <file>", short: "Size the required reading of a filled brief", args: cobra.ExactArgs(1), bind: bindSize(runBriefCheck)},
		{use: "size <file>", short: "Report what a brief makes a lane read", args: cobra.ExactArgs(1), bind: bindSize(runBriefSize)},
	}
}

// bindSize binds the flags of the size commands and passes them to the body.
func bindSize(run func(input, brief.SizeOptions) error) func(*pflag.FlagSet) func(input) error {
	return func(f *pflag.FlagSet) func(input) error {
		opts := &brief.SizeOptions{}
		f.StringVar(&opts.Root, "root", "", "the round folder")
		f.IntVar(&opts.Cap, "cap", 0, "the lane context cap in tokens (default: the standard cap)")

		return func(in input) error { return run(in, *opts) }
	}
}

// briefNewOptions holds the flags of brief new.
type briefNewOptions struct {
	lane     laneFlags
	kind     string
	bodyFile string
	slots    string
	rules    string
	out      string
	project  projectFlags
}

// laneFlags holds one value for each flag that fills a brief.Lane. laneOf copies them into the lane.
type laneFlags struct {
	key, mechanism, acceptance, baseResult  string
	handoff, transcript, reviewPR, expected string
	branch, readFirst                       string
	cap                                     int
	newBehavior, union                      bool
}

func bindBriefNew(f *pflag.FlagSet) func(input) error {
	opts := &briefNewOptions{}
	bindLaneIdentity(f, opts)
	bindLaneDetail(f, &opts.lane)
	bindBriefFiles(f, opts)
	opts.project.bind(f)

	return func(in input) error { return runBriefNew(in, opts) }
}

func bindLaneIdentity(f *pflag.FlagSet, opts *briefNewOptions) {
	f.StringVar(&opts.lane.key, "key", "", "the pull request number, or the item before it exists")
	f.StringVar(&opts.kind, "lane-kind", string(brief.LaneFix), "fix, rework or relay")
	f.StringVar(&opts.lane.mechanism, "mechanism", "", "the mechanism the lane patches")
	f.StringVar(&opts.lane.acceptance, "acceptance", "", "what makes the lane done")
	f.StringVar(&opts.lane.baseResult, "base-result", "", "the result on the base")
}

func bindLaneDetail(f *pflag.FlagSet, lane *laneFlags) {
	f.BoolVar(&lane.newBehavior, "new-behavior", false, "the lane adds behavior")
	f.StringVar(&lane.handoff, "handoff", "", "the handoff of the lane before this one")
	f.StringVar(&lane.transcript, "transcript", "", "the transcript of the lane before this one")
	f.IntVar(&lane.cap, "cap", 0, "the lane context cap in tokens (default: the standard cap)")
	f.StringVar(&lane.reviewPR, "review-pr", "", "the pull request a review lane reads")
	f.StringVar(&lane.expected, "expected", "", "the expected result")
	f.BoolVar(&lane.union, "union", false, "the lane reviews a union of pull requests")
	f.StringVar(&lane.branch, "branch", "", "the branch of the lane")
	f.StringVar(&lane.readFirst, "read-first", "", "the files to read first")
}

func bindBriefFiles(f *pflag.FlagSet, opts *briefNewOptions) {
	f.StringVar(&opts.bodyFile, "body-file", "", "a file that holds the pull request body")
	f.StringVar(&opts.slots, "slots", "", "a file of NAME=value slots")
	f.StringVar(&opts.rules, "rules", "", "the rules file of the project")
	f.StringVar(&opts.out, "out", "", "write the brief to this file (default: stdout)")
}

// fileText reads the file at a path.
func fileText(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the user names the path on the command line

	return string(data), err
}

// fileReader returns a reader of the file at a path.
func fileReader(path string) func() (string, error) {
	return func() (string, error) { return fileText(path) }
}

func laneOf(opts *briefNewOptions) (brief.Lane, error) {
	kind, err := brief.ParseLaneKind(opts.kind)
	if err != nil {
		return brief.Lane{}, err
	}

	var body func() (string, error)
	if opts.bodyFile != "" {
		body = fileReader(opts.bodyFile)
	}

	flags := opts.lane

	return brief.Lane{
		Key: flags.key, Kind: kind, Mechanism: flags.mechanism, Acceptance: flags.acceptance, BaseResult: flags.baseResult,
		NewBehavior: flags.newBehavior, Handoff: flags.handoff, Transcript: flags.transcript, Cap: flags.cap, ReviewPR: flags.reviewPR,
		Expected: flags.expected, Union: flags.union, Branch: flags.branch, ReadFirst: flags.readFirst, Body: body,
	}, nil
}

func openLedger(env map[string]string) (*ledger.Ledger, error) {
	dir, err := config.StateDir(env)
	if err != nil {
		return nil, err
	}

	return ledger.Open(dir), nil
}

func runBriefNew(in input, opts *briefNewOptions) error {
	request, err := briefRequest(in, opts)
	if err != nil {
		return err
	}

	generated, err := generate(in, request)
	if err != nil {
		return err
	}

	if err := emit(in.stdout, opts.out, generated.Text); err != nil {
		return err
	}

	say(in.stderr, generated.Ledger...)

	return nil
}

func generate(in input, request brief.Request) (brief.Generated, error) {
	book, err := openLedger(in.env)
	if err != nil {
		return brief.Generated{}, err
	}

	return brief.Generate(request, brief.Deps{Book: book, Handoff: handoffCheck})
}

func briefRequest(in input, opts *briefNewOptions) (brief.Request, error) {
	kind, err := brief.ParseKind(in.args[0])
	if err != nil {
		return brief.Request{}, err
	}

	lane, err := laneOf(opts)
	if err != nil {
		return brief.Request{}, err
	}

	if lane.Branch, err = baseBranch(in, opts, lane.Branch); err != nil {
		return brief.Request{}, err
	}

	return brief.Request{
		Kind: kind, Name: in.args[1], Model: in.args[2], Lane: lane, At: time.Now(), RulesPath: opts.rules, SlotsFile: opts.slots,
	}, nil
}

// baseBranch returns the --branch flag, else the trunk of the project the flags choose, else none.
func baseBranch(in input, opts *briefNewOptions, branch string) (string, error) {
	if branch != "" || !opts.project.given() {
		return branch, nil
	}

	chosen, err := opts.project.load(in.env)

	return chosen.BaseBranch, err
}

// emit writes the text to the file at out, or to stdout when out is empty.
func emit(stdout io.Writer, out, text string) error {
	if out == "" {
		_, _ = fmt.Fprint(stdout, text)

		return nil
	}

	if err := os.WriteFile(out, []byte(text), fileMode); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	return nil
}

// handoffCheck judges the handoff of the lane before this one with the papers check.
func handoffCheck(text, name, transcript string, tokens int) ([]string, bool, error) {
	opts := papers.HandoffOptions{Transcript: transcript}
	if tokens > 0 {
		capText := strconv.Itoa(tokens)
		opts.Cap = &capText
	}

	result, err := papers.Handoff(text, name, opts)

	return result.Lines, result.Refused, err
}

func printReport(in input, report brief.Report, err error) error {
	say(in.stdout, report.Out...)
	say(in.stderr, report.Warn...)

	return err
}

func runBriefCheck(in input, opts brief.SizeOptions) error {
	report, err := brief.Check(in.args[0], opts)

	return printReport(in, report, err)
}

func runBriefSize(in input, opts brief.SizeOptions) error {
	report, err := brief.Size(in.args[0], opts)

	return printReport(in, report, err)
}

func ledgerSpecs(deps CheckDeps) []spec {
	return []spec{
		{use: "add <key> <lane> <kind>", short: "Add the row of a lane", args: cobra.ExactArgs(threeArgs), bind: bindLedgerAdd(deps)},
		{use: "count <key> <kinds>", short: "Count the rows of a key by kind", args: cobra.ExactArgs(twoArgs), run: runLedgerCount},
		{use: "rounds <key> <mechanism>", short: "Count the patch rounds of a mechanism", args: cobra.ExactArgs(twoArgs), run: runLedgerRounds},
		{use: "prior <key> <lane>", short: "Count the lanes before this one", args: cobra.ExactArgs(twoArgs), run: runLedgerPrior},
		{use: "closes <key>", short: "List the issues a key closes", args: cobra.ExactArgs(1), run: runLedgerCloses},
	}
}

// ledgerAddOptions holds the flags of ledger add.
type ledgerAddOptions struct {
	request  ledger.Request
	bodyFile string
	pr       int
}

func bindLedgerAdd(deps CheckDeps) func(*pflag.FlagSet) func(input) error {
	return func(f *pflag.FlagSet) func(input) error {
		opts := &ledgerAddOptions{}
		bindLedgerAddFlags(f, opts)

		return func(in input) error { return runLedgerAdd(in, opts, deps) }
	}
}

func bindLedgerAddFlags(f *pflag.FlagSet, opts *ledgerAddOptions) {
	f.StringVar(&opts.request.Mechanism, "mechanism", "", "the mechanism of a fix lane")
	f.StringVar(&opts.request.Model, "model", "", "the model of a fix lane")
	f.IntVar(&opts.request.Round, "round", 0, "the round of a fix lane")
	f.StringVar(&opts.request.Words, "words", "", "the words of the owner, for an owner row")
	f.StringVar(&opts.bodyFile, "body-file", "", "a file that holds the pull request body")
	f.IntVar(&opts.pr, "pr", 0, "the pull request to read the body from, when the key is a name")
}

func ledgerRequest(in input, opts *ledgerAddOptions, deps CheckDeps) (ledger.Request, error) {
	kind, err := ledger.ParseKind(in.args[2])
	if err != nil {
		return ledger.Request{}, err
	}

	request := opts.request
	request.Key, request.Lane, request.Kind, request.At = in.args[0], in.args[1], kind, time.Now()

	request.Body, err = bodyReader(in, opts, deps)

	return request, err
}

// bodyReader returns the reader of the pull request body: the file of --body-file, else the pull request of --pr, else none.
func bodyReader(in input, opts *ledgerAddOptions, deps CheckDeps) (read func() (string, error), err error) {
	repo := config.NewSettings(in.env).Repo()

	switch {
	case opts.bodyFile != "":
		read = fileReader(opts.bodyFile)
	case opts.pr != 0 && deps.PRBody == nil:
		err = errors.New("this build cannot read a pull request: use --body-file")
	case opts.pr != 0:
		read = func() (string, error) { return deps.PRBody(in.ctx, repo, opts.pr) }
	}

	return read, err
}

func runLedgerAdd(in input, opts *ledgerAddOptions, deps CheckDeps) error {
	request, err := ledgerRequest(in, opts, deps)
	if err != nil {
		return err
	}

	book, err := openLedger(in.env)
	if err != nil {
		return err
	}

	added, err := book.Add(request)
	if err != nil {
		return err
	}

	say(in.stdout, added.Closes...)

	return nil
}

func runLedgerCount(in input) error {
	book, err := openLedger(in.env)
	if err != nil {
		return err
	}

	count, err := book.Count(in.args[0], in.args[1])
	if err != nil {
		return err
	}

	say(in.stdout, strconv.Itoa(count))

	return nil
}

func runLedgerRounds(in input) error {
	book, err := openLedger(in.env)
	if err != nil {
		return err
	}

	rounds, err := book.Rounds(in.args[0], in.args[1])
	if err != nil {
		return err
	}

	say(in.stdout, fmt.Sprintf("%d open=%t", rounds.Count, rounds.Open))

	return nil
}

func runLedgerPrior(in input) error {
	book, err := openLedger(in.env)
	if err != nil {
		return err
	}

	prior, err := book.Prior(in.args[0], in.args[1])
	if err != nil {
		return err
	}

	say(in.stdout, strconv.Itoa(prior))

	return nil
}

func runLedgerCloses(in input) error {
	book, err := openLedger(in.env)
	if err != nil {
		return err
	}

	closes, err := book.Closes(in.args[0])
	if err != nil {
		return err
	}

	say(in.stdout, closes...)

	return nil
}
