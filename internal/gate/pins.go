package gate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// toolName is a program name that the gate starts by name. A pin file line with another name is refused, so a name never reaches the shell as code.
var toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// pin is one line of the pin file: a tool and the version that the project pins.
type pin struct {
	tool    string
	version string
}

// readPins reads the pin file as it is at the base commit, origin/<base branch>, and not as it is in the tree of the lane: a lane must not be able to edit its own pins.
// The format is the one of .tool-versions: one tool and its version on each line, a # starts a comment.
// A file that is absent at the base commit, a line without a version, a name that is not a program name, or a file with no line is an error.
func (g Gate) readPins(ctx context.Context, dir, base, file string) ([]pin, error) {
	data, code := g.shell(ctx, dir, "git show "+quote("origin/"+base+":"+file))
	if code != 0 {
		return nil, fmt.Errorf("the pin file %s cannot be read at origin/%s (exit code %d): %s", file, base, code, firstLine(data))
	}

	pins, err := parsePins(file, data)
	if err == nil && len(pins) == 0 {
		return nil, fmt.Errorf("the pin file %s pins no tool. Add a line with a tool name and a version", file)
	}

	return pins, err
}

func parsePins(file, text string) ([]pin, error) {
	var pins []pin

	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if !validLine(fields) {
			return nil, fmt.Errorf("the pin file %s has the line %q. Write each line as a tool name and a version", file, strings.TrimSpace(line))
		}

		pins = append(pins, pin{tool: fields[0], version: fields[1]})
	}

	return pins, nil
}

func validLine(fields []string) bool { return len(fields) >= 2 && toolName.MatchString(fields[0]) }

// pinProblem returns the first reason why a tool does not run at the pinned version, or empty when every pinned tool does.
// obee asks the tool itself with `<tool> version`, then `<tool> --version`, and never trusts a name on the path.
// A tool whose version cannot be read is a problem too: the check fails closed.
func (g Gate) pinProblem(ctx context.Context, request Request) string {
	dir, file := request.Dir, request.Project.PinFile
	if file == "" {
		return ""
	}

	pins, err := g.readPins(ctx, dir, request.Project.BaseBranch, file)
	if err != nil {
		return err.Error()
	}

	for _, p := range pins {
		if reason := g.toolProblem(ctx, dir, p); reason != "" {
			return reason
		}
	}

	return ""
}

// installed asks the tool for its version, the same way for the pin check and for the gate key.
func (g Gate) installed(ctx context.Context, dir string, p pin) (string, int) {
	return g.shell(ctx, dir, quote(p.tool)+" version 2>&1 || "+quote(p.tool)+" --version 2>&1")
}

func (g Gate) toolProblem(ctx context.Context, dir string, p pin) string {
	output, code := g.installed(ctx, dir, p)

	return versionProblem(p, output, code)
}

// versionProblem is the reason why the version text of an installed tool is not the pinned version, or empty when it is.
func versionProblem(p pin, output string, code int) string {
	if code != 0 {
		return fmt.Sprintf("the tool %s pins version %s, but its version cannot be read (exit code %d)", p.tool, p.version, code)
	}

	if !hasVersion(output, p) {
		return fmt.Sprintf("the tool %s pins version %s, but the tool says: %s", p.tool, p.version, firstLine(output))
	}

	return ""
}

// hasVersion is true when a word of the output is the pinned version, with or without a leading "v" or the tool name ("go1.27.3", "v1.27.3").
func hasVersion(output string, p pin) bool {
	want := bare(p.version, p.tool)

	for _, word := range strings.Fields(output) {
		if bare(word, p.tool) == want {
			return true
		}
	}

	return false
}

func bare(word, tool string) string {
	return strings.TrimPrefix(strings.TrimPrefix(word, tool), "v")
}

func firstLine(output string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")

	return line
}
