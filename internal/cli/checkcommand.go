package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

const (
	forbidFlag   = "forbid"
	bashToolName = "Bash"
	exitRefused  = 2
)

var errUnknownKind = errors.New("unknown kind of command")

// hookEvent is the part of a PreToolUse event that the check reads. Claude Code sends it as JSON on stdin.
type hookEvent struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func newCheckCommandCommand() *cobra.Command {
	var forbid []string

	command := &cobra.Command{
		Use:   "command",
		Short: "Refuse a shell command of an agent that breaks a rule, as a PreToolUse hook",
		Long: "Read one PreToolUse event as JSON on stdin and check the Bash command in it against the shell rules. " +
			"Exit 0 when the command is allowed or the tool is not Bash. Exit 2 and print the reason on stderr when the command breaks a rule or the command fails for any other reason (an unreadable event, a bad flag); " +
			"Claude Code then blocks the call. The step of a workflow installs this command as a hook for each run that sets `forbid_commands`.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckCommand(cmd.InOrStdin(), cmd.ErrOrStderr(), forbid)
		},
	}
	// A hook that fails must block the call, so a bad flag ends with exit code 2 as well.
	command.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return refuse(cmd.ErrOrStderr(), err.Error())
	})
	// git-write is opt-in: a bare hook must not refuse every commit.
	defaults := slices.DeleteFunc(model.FindingKindNames(), func(name string) bool { return name == string(model.FindingGitWrite) })
	command.Flags().StringSliceVar(&forbid, forbidFlag, defaults, "the kinds of command to refuse, separated by commas; the default is every kind except git-write ("+strings.Join(model.FindingKindNames(), ", ")+")")

	return command
}

// noArgs refuses an argument with exit code 2, as every other failure of the command does.
func noArgs(cmd *cobra.Command, args []string) error {
	if cobra.NoArgs(cmd, args) != nil {
		return refuse(cmd.ErrOrStderr(), "the command takes no argument")
	}

	return nil
}

func runCheckCommand(stdin io.Reader, stderr io.Writer, forbid []string) error {
	kinds, err := kindsOf(forbid)
	if err != nil {
		return refuse(stderr, err.Error())
	}

	event, err := decodeEvent(stdin)
	if err != nil {
		return refuse(stderr, err.Error())
	}

	if event.ToolName != bashToolName {
		return nil
	}

	if hits := hitsOf(event.ToolInput.Command, kinds); len(hits) > 0 {
		return refuse(stderr, "this command breaks the shell rules of the step.\n"+strings.Join(hits, "\n"))
	}

	return nil
}

func decodeEvent(stdin io.Reader) (hookEvent, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return hookEvent{}, fmt.Errorf("cannot read the hook event: %w", err)
	}

	return model.Decode[hookEvent]("the hook event", string(raw))
}

// hitsOf returns one line for each finding of a listed kind in the command.
func hitsOf(command string, kinds []model.FindingKind) []string {
	var hits []string

	for _, finding := range shell.ScanFor(command, kinds) {
		hits = append(hits, fmt.Sprintf("%s: %s", finding.Kind, finding.Text))
	}

	return hits
}

func kindsOf(names []string) ([]model.FindingKind, error) {
	known := model.FindingKindNames()
	kinds := make([]model.FindingKind, 0, len(names))

	for _, name := range names {
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("%w %q (use one of: %s)", errUnknownKind, name, strings.Join(known, ", "))
		}

		kinds = append(kinds, model.FindingKind(name))
	}

	return kinds, nil
}

// refuse prints the reason on stderr, where Claude Code reads it, and ends with exit code 2, which blocks the call.
func refuse(stderr io.Writer, reason string) error {
	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintln(stderr, "obee refused the command: "+reason)

	return &ExitError{Code: exitRefused}
}
