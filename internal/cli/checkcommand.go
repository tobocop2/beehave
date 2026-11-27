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

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/check/shell"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	forbidFlag  = "forbid"
	protectFlag = "protect"
	// visibilityFlag tells the check whether the repository is private. A missing flag counts as public.
	visibilityFlag = "visibility"
	// agentFlag names the agent CLI whose hook format the command reads.
	agentFlag   = "agent"
	exitRefused = 2
	// maxEventBytes is the most that the command reads from stdin. A longer event is refused.
	maxEventBytes = 1 << 20
)

var (
	errUnknownAgent = errors.New("unknown agent")
	errEventTooLong = errors.New("the hook event is too long")
	errUnknownKind  = errors.New("unknown kind of command")
	errNoPaths      = errors.New("the kind needs a directory")
)

func newCheckCommandCommand(hooks map[model.Agent]agent.HookDecoder) *cobra.Command {
	var forbid, protect []string

	var visibility, agentName string

	command := &cobra.Command{
		Use:   "command",
		Short: "Refuse a shell command of an agent that breaks a rule, as a hook of the agent CLI",
		Long:  checkCommandLong,
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHook(cmd, hooks, agentName, forbid, protect, visibility)
		},
	}
	// A hook that fails must block the call, so a bad flag ends with exit code 2 as well.
	command.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return refuse(cmd.ErrOrStderr(), err.Error())
	})
	addAgentFlag(command, &agentName, hooks)
	addCheckFlags(command, &forbid, &protect, &visibility)

	return command
}

const checkCommandLong = "Read one hook event of the agent CLI that --agent names, as it arrives on stdin, and check the shell command in it against the shell rules. " +
	"Exit 0 only when the event is a shell call with a command that breaks no rule. Exit 2 and print the reason on stderr when the command breaks a rule or the command fails for any other reason " +
	"(an unknown agent, an event that is not a shell call or has no command, an event longer than 1 MiB, an unreadable event, a bad flag); " +
	"the agent CLI then blocks the call. The step of a workflow installs this command as a hook for each run that sets `forbid_commands`. " +
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

// runHook finds the hook reader of the agent that --agent names, and checks the event on stdin with it.
func runHook(cmd *cobra.Command, hooks map[model.Agent]agent.HookDecoder, name string, forbid, protect []string, visibility string) error {
	decode, err := hookReader(hooks, name)
	if err != nil {
		return refuse(cmd.ErrOrStderr(), err.Error())
	}

	return runCheckCommand(cmd.InOrStdin(), cmd.ErrOrStderr(), decode, forbid, protect, visibility)
}

// addAgentFlag adds --agent, which names the agent CLI whose hook format the command reads.
func addAgentFlag(command *cobra.Command, name *string, hooks map[model.Agent]agent.HookDecoder) {
	command.Flags().StringVar(name, agentFlag, "", "the agent CLI that sends the event, whose hook format the command reads ("+strings.Join(hookNames(hooks), ", ")+")")
}

// hookNames returns the names of the agents that --agent accepts, in order.
func hookNames(hooks map[model.Agent]agent.HookDecoder) []string {
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, string(name))
	}

	slices.Sort(names)

	return names
}

// hookReader returns the hook reader of the agent that --agent names. A missing or unknown name is an error that lists the known ones.
func hookReader(hooks map[model.Agent]agent.HookDecoder, name string) (agent.HookDecoder, error) {
	if decode := hooks[model.Agent(name)]; decode != nil {
		return decode, nil
	}

	return nil, fmt.Errorf("%w %q (use one of: %s)", errUnknownAgent, name, strings.Join(hookNames(hooks), ", "))
}

// dirOf returns the directory the command of the call runs in: the one of the call, else the one of the process. With none, a relative path cannot be placed and counts against the command.
func dirOf(call agent.HookCall, getwd func() (string, error)) string {
	if call.Dir != "" {
		return call.Dir
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

func runCheckCommand(stdin io.Reader, stderr io.Writer, decode agent.HookDecoder, forbid, protect []string, visibility string) error {
	kinds, private, err := settingsOf(forbid, protect, visibility)
	if err != nil {
		return refuse(stderr, err.Error())
	}

	call, err := decodeEvent(stdin, decode)
	if err != nil {
		return refuse(stderr, err.Error())
	}

	if hits := hitsOf(call.Command, kinds, shell.Protection{Dirs: protect, Base: dirOf(call, os.Getwd), Private: private}); len(hits) > 0 {
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

func decodeEvent(stdin io.Reader, decode agent.HookDecoder) (agent.HookCall, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxEventBytes+1))
	if err != nil {
		return agent.HookCall{}, fmt.Errorf("cannot read the hook event: %w", err)
	}

	if len(raw) > maxEventBytes {
		return agent.HookCall{}, fmt.Errorf("%w: it is longer than %d bytes", errEventTooLong, maxEventBytes)
	}

	return decode(string(raw))
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

// refuse prints the reason on stderr, where the agent CLI reads it, and ends with exit code 2, which blocks the call.
func refuse(stderr io.Writer, reason string) error {
	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintln(stderr, "beehest refused the command: "+reason)

	return &ExitError{Code: exitRefused}
}
