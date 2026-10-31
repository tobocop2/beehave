package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

const (
	forbidFlag  = "forbid"
	protectFlag = "protect"
	// visibilityFlag tells the check whether the repository is private. A missing flag counts as public.
	visibilityFlag = "visibility"
	bashToolName   = "Bash"
	exitRefused    = 2
)

var (
	errUnknownKind = errors.New("unknown kind of command")
	errNoPaths     = errors.New("the kind needs a directory")
)

// hookEvent is the part of a PreToolUse event that the check reads. Claude Code sends it as JSON on stdin.
// Cwd is the directory the Bash tool runs the command in.
type hookEvent struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func newCheckCommandCommand() *cobra.Command {
	var forbid, protect []string

	var visibility string

	command := &cobra.Command{
		Use:   "command",
		Short: "Refuse a shell command of an agent that breaks a rule, as a PreToolUse hook",
		Long:  checkCommandLong,
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckCommand(cmd.InOrStdin(), cmd.ErrOrStderr(), forbid, protect, visibility)
		},
	}
	// A hook that fails must block the call, so a bad flag ends with exit code 2 as well.
	command.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return refuse(cmd.ErrOrStderr(), err.Error())
	})
	addCheckFlags(command, &forbid, &protect, &visibility)

	return command
}

const checkCommandLong = "Read one PreToolUse event as JSON on stdin and check the Bash command in it against the shell rules. " +
	"Exit 0 when the command is allowed or the tool is not Bash. Exit 2 and print the reason on stderr when the command breaks a rule or the command fails for any other reason (an unreadable event, a bad flag); " +
	"Claude Code then blocks the call. The step of a workflow installs this command as a hook for each run that sets `forbid_commands`. " +
	"The kind protected-path refuses a write inside the directories of --protect, and a git command that is not a read there. " +
	"The kind laptop-copy refuses a copy from this machine to another machine (rsync or scp with a remote target, a pipe into ssh) unless --visibility is private; " +
	"a missing --visibility counts as public."

// visibilityNames returns the names that --visibility accepts.
func visibilityNames() []string {
	names := make([]string, 0, len(model.Visibilities()))
	for _, v := range model.Visibilities() {
		names = append(names, string(v))
	}

	return names
}

func addCheckFlags(command *cobra.Command, forbid, protect *[]string, visibility *string) {
	command.Flags().StringVar(visibility, visibilityFlag, "", "whether the repository is "+strings.Join(visibilityNames(), " or ")+"; empty counts as public, and only private lets the kind laptop-copy pass a copy")
	// git-write is opt-in: a bare hook must not refuse every commit. protected-path is opt-in too: it has no directory until --protect gives one.
	defaults := slices.DeleteFunc(model.FindingKindNames(), func(name string) bool {
		return name == string(model.FindingGitWrite) || model.FindingKind(name).NeedsPaths()
	})
	command.Flags().StringSliceVar(forbid, forbidFlag, defaults, "the kinds of command to refuse, separated by commas; the default is every kind except git-write and protected-path ("+strings.Join(model.FindingKindNames(), ", ")+")")
	command.Flags().StringSliceVar(protect, protectFlag, nil, "the absolute directories that protected-path guards, separated by commas; the kind needs at least one")
}

// dir returns the directory the command of the event runs in. With none, a relative path cannot be placed and counts against the command.
func (e hookEvent) dir(getwd func() (string, error)) string {
	if e.Cwd != "" {
		return e.Cwd
	}

	dir, err := getwd()
	if err != nil {
		return ""
	}

	return dir
}

// noArgs refuses an argument with exit code 2, as every other failure of the command does.
func noArgs(cmd *cobra.Command, args []string) error {
	if cobra.NoArgs(cmd, args) != nil {
		return refuse(cmd.ErrOrStderr(), "the command takes no argument")
	}

	return nil
}

func runCheckCommand(stdin io.Reader, stderr io.Writer, forbid, protect []string, visibility string) error {
	kinds, private, err := settingsOf(forbid, protect, visibility)
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

	if hits := hitsOf(event.ToolInput.Command, kinds, shell.Protection{Dirs: protect, Base: event.dir(os.Getwd), Private: private}); len(hits) > 0 {
		return refuse(stderr, "this command breaks the shell rules of the step.\n"+strings.Join(hits, "\n"))
	}

	return nil
}

// settingsOf reads the kinds of --forbid and the --visibility value.
func settingsOf(forbid, protect []string, visibility string) ([]model.FindingKind, bool, error) {
	kinds, err := kindsOf(forbid, protect)
	if err != nil {
		return nil, false, err
	}

	private, err := privateOf(visibility)

	return kinds, private, err
}

// privateOf reads the --visibility value. Empty is unknown and counts as public, so the check fails closed. A value that names no visibility is refused.
func privateOf(visibility string) (bool, error) {
	if visibility == "" {
		return false, nil
	}

	parsed, err := model.ParseVisibility(visibility)
	if err != nil {
		return false, fmt.Errorf("--%s: %w", visibilityFlag, err)
	}

	return parsed == model.VisibilityPrivate, nil
}

func decodeEvent(stdin io.Reader) (hookEvent, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return hookEvent{}, fmt.Errorf("cannot read the hook event: %w", err)
	}

	return model.Decode[hookEvent]("the hook event", string(raw))
}

// hitsOf returns one line for each finding of a listed kind in the command.
func hitsOf(command string, kinds []model.FindingKind, protect shell.Protection) []string {
	var hits []string

	for _, finding := range shell.ScanWith(command, kinds, protect) {
		hits = append(hits, fmt.Sprintf("%s: %s", finding.Kind, finding.Text))
	}

	return hits
}

// needPaths refuses a kind that judges against a list of directories when the list is empty, and a directory that is not absolute.
// A hook with an empty list would pass every command, so it must fail.
func needPaths(kinds []model.FindingKind, protect []string) error {
	for _, kind := range kinds {
		if kind.NeedsPaths() && len(protect) == 0 {
			return fmt.Errorf("%w: --%s lists %q, so give at least one directory with --%s", errNoPaths, forbidFlag, kind, protectFlag)
		}
	}

	for _, dir := range protect {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("--%s needs absolute directories, and %q is not one", protectFlag, dir)
		}
	}

	return nil
}

// kindsOf reads the names of the --forbid flag, and checks them against the directories of --protect.
func kindsOf(names, protect []string) ([]model.FindingKind, error) {
	known := model.FindingKindNames()
	kinds := make([]model.FindingKind, 0, len(names))

	for _, name := range names {
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("%w %q (use one of: %s)", errUnknownKind, name, strings.Join(known, ", "))
		}

		kinds = append(kinds, model.FindingKind(name))
	}

	return kinds, needPaths(kinds, protect)
}

// refuse prints the reason on stderr, where Claude Code reads it, and ends with exit code 2, which blocks the call.
func refuse(stderr io.Writer, reason string) error {
	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintln(stderr, "obee refused the command: "+reason)

	return &ExitError{Code: exitRefused}
}
