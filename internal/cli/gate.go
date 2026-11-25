package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/ownprogram"
)

const ownProgramFlag = "own-program"

// maxGateArgs is the pull request and the optional project.
const maxGateArgs = 2

// OwnProgram builds the effects of the owner command for one project: the forge, the paths that a head changes, the gate and the writer that posts.
// A nil value leaves the gate command out.
type OwnProgram func(ctx context.Context, cfg config.Config, opts flow.Options, project string) (ownprogram.Deps, error)

// Console gives the input of the gate command and says whether that input and the output are a terminal.
type Console func() (io.Reader, bool)

// processConsole is the console of the process: its standard input, and whether the standard input and the standard output are a terminal.
func processConsole() (io.Reader, bool) {
	return os.Stdin, isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

const gateLong = "Run the gate of one pull request with the gate program of its own head, and post the result as the status beehave/gate on the head.\n" +
	"Use it for a pull request that changes the files that gate_program lists, which the normal gate refuses. The command runs only on a terminal.\n" +
	"It prints the diff of the pull request and runs the gate after you type the number of the pull request.\n" +
	"The command refuses a pull request on the branch of an item, a pull request into a branch that is not the base branch of the project,\n" +
	"a head that is behind the base branch, a change of a submodule, and a change of a path that gate_program does not list.\n" +
	"It posts nothing when the head or the base branch moved while the gate ran."

// pullNumber reads the number of the pull request from the arguments: the first, with an optional project after it.
func pullNumber(args []string) (int, error) {
	if len(args) < 1 || len(args) > maxGateArgs {
		return 0, errors.New("give the number of the pull request, and the project when the config holds more than one: beehave gate --" + ownProgramFlag + " <pull request> [project]")
	}

	number, err := strconv.Atoi(args[0])
	if err != nil || number < 1 {
		return 0, fmt.Errorf("%q is not a pull request number", args[0])
	}

	return number, nil
}

// pullArgs checks the arguments of the gate command and stores the number of the pull request in number.
func pullArgs(number *int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) (err error) {
		*number, err = pullNumber(args)

		return err
	}
}

func (f flowCommands) gateCommand() *cobra.Command {
	var (
		flag   string
		own    bool
		number int
	)

	command := &cobra.Command{
		Use:   "gate --" + ownProgramFlag + " <pull request> [project]",
		Short: "Run the gate of a pull request with its own gate program and post the result",
		Long:  gateLong,
		Args:  pullArgs(&number),
		RunE:  func(cmd *cobra.Command, args []string) error { return f.gate(cmd, own, flag, number, args[1:]) },
	}
	addConfigFlag(command, &flag)
	command.Flags().BoolVar(&own, ownProgramFlag, false, "judge the pull request with the gate program of its own head (required)")

	return command
}

// gate runs the owner command for one pull request and prints what the gate said.
func (f flowCommands) gate(cmd *cobra.Command, own bool, flag string, number int, named []string) error {
	if !own {
		return errors.New("the gate command runs only with --" + ownProgramFlag + ", which judges the pull request with its own gate program")
	}

	in, err := f.terminal()
	if err != nil {
		return err
	}

	out, err := f.judgeNamed(cmd, flag, named, number, in)
	if err != nil {
		return err
	}

	return sayGate(cmd.OutOrStdout(), cmd.ErrOrStderr(), number, out)
}

// judgeNamed reads the config, picks the project and runs the command for it.
func (f flowCommands) judgeNamed(cmd *cobra.Command, flag string, named []string, number int, in io.Reader) (ownprogram.Outcome, error) {
	cfg, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return ownprogram.Outcome{}, err
	}

	project, err := chooseProject(cfg, named)
	if err != nil {
		return ownprogram.Outcome{}, err
	}

	return f.judge(cfg, opts, project, number, owner{in: in, out: cmd.OutOrStdout()})
}

// terminal returns the input of the command, or the refusal when the input and the output are not a terminal. A script cannot confirm a diff.
func (f flowCommands) terminal() (io.Reader, error) {
	console := f.console
	if console == nil {
		console = processConsole
	}

	in, ok := console()
	if !ok {
		return nil, errors.New("the gate command runs only on a terminal: the owner reads the diff and types the number of the pull request")
	}

	return in, nil
}

// judge builds the request and the effects of the command, adds the confirmation of the owner and runs it.
func (f flowCommands) judge(cfg config.Config, opts flow.Options, project string, number int, who owner) (ownprogram.Outcome, error) {
	request, err := f.gateRequest(cfg, opts, project, number)
	if err != nil {
		return ownprogram.Outcome{}, err
	}

	deps, err := f.ownProgram(f.ctx, cfg, opts, project)
	if err != nil {
		return ownprogram.Outcome{}, err
	}

	deps.Confirm = who.confirm

	return ownprogram.Run(f.ctx, deps, request)
}

// owner is the person at the terminal: what they read and where the command writes the diff.
type owner struct {
	in  io.Reader
	out io.Writer
}

// confirm prints the diff of the pull request and asks the owner to type its number. Any other answer, or no answer, is a refusal.
func (o owner) confirm(_ context.Context, pr int, head, diff string) error {
	// A failed write to the terminal has no recovery.
	_, _ = fmt.Fprintf(o.out, "Diff of pull request %d at %s against the base branch:\n\n%s\n\nType %d to run the gate with this program and post the status: ", pr, head, plain(diff), pr)

	scanner := bufio.NewScanner(o.in)
	scanner.Scan()

	if strings.TrimSpace(scanner.Text()) != strconv.Itoa(pr) {
		return fmt.Errorf("%w: type the number of the pull request, %d, to confirm", ownprogram.ErrNotConfirmed, pr)
	}

	return nil
}

// plain writes each hidden character of text as a visible escape such as <U+202E>: the control characters except the line break and the tab, the format
// characters (bidi overrides, zero-width characters) and the line and paragraph separators. So the diff cannot move the cursor, hide a line or show its
// text in another order on the terminal.
func plain(text string) string {
	var out strings.Builder

	for _, r := range text {
		if r != '\n' && r != '\t' && unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			// A write to a strings.Builder never fails.
			_, _ = fmt.Fprintf(&out, "<U+%04X>", r)

			continue
		}

		out.WriteRune(r)
	}

	return out.String()
}

// gateRequest is the request of the command: the project and the branches of its items.
func (f flowCommands) gateRequest(cfg config.Config, opts flow.Options, project string, number int) (ownprogram.Request, error) {
	branches, err := f.itemBranches(cfg, opts, project)

	return ownprogram.Request{Project: cfg.Projects[project].Project, PR: number, ItemBranches: branches}, err
}

// itemBranches lists the branches of the items that the runs of the project name. The command adds the open items of the tracker of the project.
// The engine names the branch of an item for the item (model.ItemBranch). A run whose project the store cannot read counts for every project.
func (f flowCommands) itemBranches(cfg config.Config, opts flow.Options, project string) ([]string, error) {
	client, err := f.factory.Client(f.ctx, cfg, opts)
	if err != nil {
		return nil, err
	}

	runs, err := client.Status()
	if err != nil {
		return nil, errors.Join(err, client.Close())
	}

	return branchesOf(runItems(runs, project)), client.Close()
}

// runItems lists the items that the runs name for the project. A run with no project counts for every project.
func runItems(runs []flow.RunInfo, project string) []string {
	var items []string

	for _, run := range runs {
		if run.Item != "" && (run.Project == "" || run.Project == project) {
			items = append(items, run.Item)
		}
	}

	return items
}

// branchesOf names the branch of each item.
func branchesOf(ids []string) []string {
	branches := make([]string, 0, len(ids))
	for _, id := range ids {
		branches = append(branches, model.ItemBranch(id))
	}

	return branches
}

// sayGate prints the verdict of the command. A gate that failed prints its report on stderr and exits with code 1.
func sayGate(stdout, stderr io.Writer, number int, out ownprogram.Outcome) error {
	if out.Passed {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintf(stdout, "pull request %d: the gate passed on %s with its own gate program. Posted beehave/gate.\n", number, out.Head)

		return nil
	}

	_, _ = fmt.Fprintf(stderr, "pull request %d: the gate failed on %s. Posted beehave/gate as failure.\n%s\n", number, out.Head, out.Report)

	return &ExitError{Code: exitError}
}
