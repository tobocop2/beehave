package gate

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// toolName is a program name that the gate starts by name. A pin file line with another name is refused, so a name never reaches the shell as code.
var toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Pin is one line of the pin file: a tool and the version that the project pins.
type Pin struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

// PinCheck is the result of the read of the pin file: the pinned tools, or the reason why the pin file cannot be used. A Submit makes it once and
// the Plan holds it, so the wait and the finish of a gate never read the pin file, its base branch or the build host of the project again.
type PinCheck struct {
	Tools []Pin  `json:"tools,omitempty"`
	Void  string `json:"void,omitempty"`
}

// readPins reads the pin file as it is at the base commit, origin/<base branch>, and not as it is in the tree of the lane: a lane must not be able to edit its own pins.
// The format is the one of .tool-versions: one tool and its version on each line, a # starts a comment.
// A file that is absent at the base commit, a line without a version, a name that is not a program name, or a file with no line is an error.
func (g Gate) readPins(ctx context.Context, dir, base, file string) ([]Pin, error) {
	data, code := g.shell(ctx, dir, "git show "+model.ShellQuote("origin/"+base+":"+file))
	if code != 0 {
		return nil, fmt.Errorf("the pin file %s cannot be read at origin/%s (exit code %d): %s", file, base, code, model.FirstLine(data))
	}

	pins, err := parsePins(file, data)
	if err == nil && len(pins) == 0 {
		return nil, fmt.Errorf("the pin file %s pins no tool. Add a line with a tool name and a version", file)
	}

	return pins, err
}

func parsePins(file, text string) ([]Pin, error) {
	var pins []Pin

	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if !validLine(fields) {
			return nil, fmt.Errorf("the pin file %s has the line %q. Write each line as a tool name and a version", file, strings.TrimSpace(line))
		}

		pins = append(pins, Pin{Tool: fields[0], Version: fields[1]})
	}

	return pins, nil
}

func validLine(fields []string) bool { return len(fields) >= 2 && toolName.MatchString(fields[0]) }

const (
	// voidMarker starts the line that the probe of a pinned tool writes when the tool is not at its pinned version. The tool and the first line of its version text follow.
	voidMarker = "obee-gate-void"

	// voidCode is the exit code of the probe that wrote the void marker.
	voidCode = 97

	// voidFields is the number of fields after the void marker: the tool, the pinned version and the text the tool gave.
	voidFields = 3
)

// pinsOf reads the pin file of the project at the base commit. A project with no pin file has no pin. A file that cannot be used gives the reason, and the gate is void.
// This is the only read of pin_file and base_branch. Submit calls it once and records the result in the Plan.
func (g Gate) pinsOf(ctx context.Context, request Request) PinCheck {
	if request.Project.PinFile == "" {
		return PinCheck{}
	}

	pins, err := g.readPins(ctx, request.Dir, request.Project.BaseBranch, request.Project.PinFile)
	if err != nil {
		return PinCheck{Void: err.Error()}
	}

	return PinCheck{Tools: pins}
}

// probe is the shell text that checks every pinned tool before the gate command. It runs in the same shell as the command, so it sees the same
// directory, environment, sandbox and host. It asks the tool itself with `<tool> version`, then `<tool> --version`, and never trusts a name on the path.
// A tool that is at another version, or whose version cannot be read, makes the shell write the void marker and exit with voidCode: the check fails closed.
func probe(pins []Pin) string {
	var text strings.Builder

	for _, p := range pins {
		tool, want := model.ShellQuote(p.Tool), model.ShellQuote(bare(p.Version, p.Tool))

		text.WriteString("v=$({ " + tool + " version 2>&1 || " + tool + " --version 2>&1; }); rc=$?; ok=; ")
		text.WriteString("if [ \"$rc\" -eq 0 ]; then set -f; for w in $v; do w=${w#" + tool + "}; w=${w#v}; [ \"$w\" = " + want + " ] && ok=1; done; set +f; fi; ")
		text.WriteString("[ -n \"$ok\" ] || { echo " + voidMarker + " " + tool + " " + model.ShellQuote(p.Version) + " \"$(printf %s \"$v\" | head -n 1)\"; exit " + strconv.Itoa(voidCode) + "; }\n")
	}

	return text.String()
}

// probed is the command with the probe of the pinned tools in front of it. A project with no pin has the command as it is.
func probed(pins []Pin, command string) string { return probe(pins) + command }

// voidReason reads the reason from the output of a command that exited with voidCode and wrote the void marker, and is empty for any other output.
func voidReason(output string) string {
	for _, line := range strings.Split(output, "\n") {
		rest, found := strings.CutPrefix(line, voidMarker+" ")
		if !found {
			continue
		}

		fields := append(strings.SplitN(rest, " ", voidFields), "", "")

		return fmt.Sprintf("the tool %s pins version %s, but the tool says: %s", fields[0], fields[1], cmp.Or(strings.TrimSpace(fields[2]), "nothing"))
	}

	return ""
}

// installed asks the tool for its version, the same way for the pin check and for the gate key.
func (g Gate) installed(ctx context.Context, dir string, p Pin) (string, int) {
	return g.shell(ctx, dir, model.ShellQuote(p.Tool)+" version 2>&1 || "+model.ShellQuote(p.Tool)+" --version 2>&1")
}

// atPin is true when the version text of an installed tool is the pinned version. A tool whose version cannot be read is not at its pin.
func atPin(p Pin, output string, code int) bool { return code == 0 && hasVersion(output, p) }

// hasVersion is true when a word of the output is the pinned version, with or without a leading "v" or the tool name ("go1.27.3", "v1.27.3").
func hasVersion(output string, p Pin) bool {
	want := bare(p.Version, p.Tool)

	for _, word := range strings.Fields(output) {
		if bare(word, p.Tool) == want {
			return true
		}
	}

	return false
}

func bare(word, tool string) string {
	return strings.TrimPrefix(strings.TrimPrefix(word, tool), "v")
}
