package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// The first words of the defect lines of a hold.
const (
	unmappedLine    = "unmapped "
	staleLine       = "stale "
	unknownStepLine = "unknown-step "
)

// Map is the gate_map of a project: each exact command text of a derived step, to the names of the gate steps that run it. An empty list says the command is not a check.
type Map map[string][]string

// Hold returns one line for each defect of the map against the derived steps and the gate step names of the round: a derived command that is no key, a key that no derived step has, then a name that is no gate step.
func Hold(steps []Step, gateSteps []string, held Map) []string {
	var lines []string

	derived := map[string]bool{}

	for _, step := range steps {
		derived[step.Command] = true

		if _, ok := held[step.Command]; !ok {
			lines = append(lines, unmappedLine+step.Name+": "+step.Command)
		}
	}

	for _, command := range slices.Sorted(maps.Keys(held)) {
		if !derived[command] {
			lines = append(lines, staleLine+command)
		}
	}

	return append(lines, unknownSteps(gateSteps, held)...)
}

// unknownSteps gives one line for each name of the map that is no gate step, sorted and once each.
func unknownSteps(gateSteps []string, held Map) []string {
	var names []string

	for _, list := range held {
		for _, name := range list {
			if !slices.Contains(gateSteps, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	lines := []string(nil)
	for _, name := range slices.Compact(names) {
		lines = append(lines, unknownStepLine+name)
	}

	return lines
}

// errMapForm is the refusal of a gate_map that is not one object of commands to lists of gate step names.
var errMapForm = errors.New("the gate_map must be one JSON object of each derived command to a list of gate step names, with no command twice, no null and no blank text")

// ParseMap reads a gate_map from its JSON text. It refuses a command given twice, which a plain decode would let the last one win, a null list, a blank command or name, and any other form.
func ParseMap(text string) (Map, error) {
	decoder, held := json.NewDecoder(strings.NewReader(text)), Map{}
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return nil, errMapForm
	}

	for decoder.More() {
		command, names, ok := mapEntry(decoder)
		if _, twice := held[command]; !ok || twice {
			return nil, fmt.Errorf("%w: %q", errMapForm, command)
		}

		held[command] = names
	}

	if !closedLast(decoder) {
		return nil, errMapForm
	}

	return held, nil
}

// closedLast reports whether the object ends next and nothing follows it.
func closedLast(decoder *json.Decoder) bool {
	closed, err := decoder.Token()
	_, end := decoder.Token()

	return err == nil && closed == json.Delim('}') && errors.Is(end, io.EOF)
}

// mapEntry reads one command of a gate_map and its list of gate step names. It gives false for a key that is not text, a blank command, a value that is not a list of text, a null list or a blank name.
func mapEntry(decoder *json.Decoder) (string, []string, bool) {
	token, err := decoder.Token()
	command, isText := token.(string)

	if err != nil || !isText || blank(command) {
		return command, nil, false
	}

	var names *[]string
	if decoder.Decode(&names) != nil || names == nil || slices.ContainsFunc(*names, blank) {
		return command, nil, false
	}

	return command, *names, true
}

// blank reports whether a text holds nothing but blanks.
func blank(text string) bool { return strings.TrimSpace(text) == "" }
