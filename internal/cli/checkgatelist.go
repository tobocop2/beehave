package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/ci"
)

const (
	gateListName   = "gate-list"
	gateListSyntax = "beehave check " + gateListName + " [--steps <name>]... [--map <json>] <ci-file>..."
	stepsFlag      = "steps"
	mapFlag        = "map"
)

const errMapNotMatch = "the gate_map of the project does not match the CI commands. Map each derived command, exactly as the step line prints it, to the gate_steps that run it or to [] when it is not a check, and remove each stale key"

var (
	errNoGateSteps = errors.New("--map holds the CI commands to gate_steps, and the project has no gate_steps. Set gate_steps in the project file, one named step for each command of the gate")
	errMapTwice    = errors.New("--map is given twice. Give the whole gate_map once")
)

const checkGateListLong = "Derive the gate steps from the run steps of the CI workflow files. " +
	"Print one line for each step, then one line for each step or job that the list leaves out " +
	"(a secret, a token that can write, an action that is not a setup, a shell other than bash or sh, a git write or a text no read can judge), then a summary line. " +
	"With --" + stepsFlag + " or --" + mapFlag + ", refuse a gate_map that does not map each derived command exactly to gate_steps names. " +
	"Exit 0 when the files give steps. Exit 2 when a file cannot be read, the files give no step, the call is wrong, or the gate_map does not match."

// gateListFlags are the gate step names of the round and the gate_map of the project, as given.
type gateListFlags struct {
	steps, maps []string
}

func newCheckGateListCommand(read ci.Reader) *cobra.Command {
	var given gateListFlags

	command := &cobra.Command{
		Use:   "gate-list [--steps <name>]... [--map <json>] <ci-file>...",
		Short: "Derive the gate steps from the CI workflow files",
		Long:  checkGateListLong,
		Args:  needCIFile,
		RunE: func(cmd *cobra.Command, paths []string) error {
			return runCheckGateList(cmd, read, given, paths)
		},
	}
	command.SetFlagErrorFunc(refuseGateList)
	command.Flags().StringArrayVar(&given.steps, stepsFlag, nil, "a name of the gate_steps of the round; give it once for each step")
	command.Flags().StringArrayVar(&given.maps, mapFlag, nil, "the gate_map of the project as JSON: each derived command to the gate_steps names that run it, or [] when it is not a check")

	return command
}

// needCIFile shows the usage and refuses a call with no CI file.
func needCIFile(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return nil
	}

	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "usage: %s\n", gateListSyntax)

	return &ExitError{Code: exitRefused}
}

// runCheckGateList derives the steps of the files and prints them. A defect of the map or of the files is exit code 2.
func runCheckGateList(cmd *cobra.Command, read ci.Reader, given gateListFlags, paths []string) error {
	lines, err := gateListLines(read, given, paths)
	if err != nil {
		return refuseGateList(cmd, err)
	}

	printGateLines(cmd.OutOrStdout(), lines)

	return nil
}

// gateListLines reads the map and the files, derives the steps and gives the lines of the list.
func gateListLines(read ci.Reader, given gateListFlags, paths []string) ([]string, error) {
	held, mapped, err := heldMap(given.maps)
	if err != nil {
		return nil, err
	}

	files, err := readCIFiles(paths)
	if err != nil {
		return nil, err
	}

	derived, err := read(files)
	if err != nil {
		return nil, err
	}

	return holdLines(stepLines(derived), derived.Steps, given.steps, held, mapped)
}

// heldMap reads the one --map that is given, and reports whether one was given.
func heldMap(maps []string) (ci.Map, bool, error) {
	switch len(maps) {
	case 0:
		return nil, false, nil
	case 1:
		held, err := ci.ParseMap(maps[0])
		if err != nil {
			return nil, true, fmt.Errorf("invalid --%s: %w", mapFlag, err)
		}

		return held, true, nil
	default:
		return nil, true, errMapTwice
	}
}

// readCIFiles reads each CI file, and refuses on the first that cannot be read or holds a NUL byte.
func readCIFiles(paths []string) ([]ci.File, error) {
	files := make([]ci.File, len(paths))

	for i, path := range paths {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			// os.ReadFile wraps the cause in a path error that repeats the path.
			return nil, fmt.Errorf("cannot read %s: %w. Check that the file exists and that you can read it", path, errors.Unwrap(err))
		}

		if bytes.IndexByte(data, 0) >= 0 {
			return nil, fmt.Errorf("%s holds a NUL byte. Rewrite it as plain text", path)
		}

		files[i] = ci.File{Path: path, Text: string(data)}
	}

	return files, nil
}

// stepLines gives one line for each derived step, one for each refused step or job, then the summary.
func stepLines(derived ci.Derived) []string {
	lines := make([]string, 0, len(derived.Steps)+len(derived.Refused)+1)
	for _, step := range derived.Steps {
		lines = append(lines, "step "+step.Name+": "+step.Command)
	}

	for _, r := range derived.Refused {
		lines = append(lines, "refused "+r.Name+": "+r.Reason)
	}

	return append(lines, fmt.Sprintf("check-gatelist: OK (steps %d, refused %d)", len(derived.Steps), len(derived.Refused)))
}

// holdLines gives the lines of the list. With --steps or --map, a map with no gate step names, or with a defect against the derived steps, is a refusal that carries the lines and one line for each defect.
func holdLines(lines []string, steps []ci.Step, gateSteps []string, held ci.Map, mapped bool) ([]string, error) {
	switch {
	case !mapped && len(gateSteps) == 0:
		return lines, nil
	case len(gateSteps) == 0:
		return nil, errNoGateSteps
	}

	if defects := ci.Hold(steps, gateSteps, held); len(defects) > 0 {
		return nil, &ci.Refusal{Message: errMapNotMatch, Lines: append(lines, defects...)}
	}

	return lines, nil
}

// refuseGateList prints the lines that a refusal carries to stdout and its message to stderr, and returns exit code 2.
func refuseGateList(cmd *cobra.Command, err error) error {
	var refusal *ci.Refusal
	if errors.As(err, &refusal) {
		printGateLines(cmd.OutOrStdout(), refusal.Lines)
	}

	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s check %s: %v\n", commandName, gateListName, err)

	return &ExitError{Code: exitRefused}
}

// printGateLines prints the lines, one each.
func printGateLines(out io.Writer, lines []string) {
	for _, line := range lines {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintln(out, line)
	}
}
