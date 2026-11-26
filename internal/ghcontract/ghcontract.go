// Package ghcontract is the contract of the pinned gh: the --json fields and the flags of each gh command that beehave runs.
// A fake of gh in a test calls Check, so a field or a flag that the real gh refuses fails a test and never reaches a user.
// The table is gh-2.101.0.txt. The live suite in internal/realtools compares it with the pinned gh at every run.
package ghcontract

import (
	_ "embed" // the table is a text file that sits beside the code
	"fmt"
	"slices"
	"strings"
)

// commandWords is how many words name a gh command in a refusal: the group and the verb.
const commandWords = 2

//go:embed gh-2.101.0.txt
var table string

// Shape is one gh command: its words, the --json fields it takes, and its flags. A flag maps to true when it takes a value.
type Shape struct {
	Words  []string
	Fields []string
	Flags  map[string]bool
}

// Shapes returns the commands of the table.
func Shapes() []Shape {
	var shapes []Shape

	for _, line := range strings.Split(table, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		words, rest, _ := strings.Cut(line, " | ")
		fields, flags, _ := strings.Cut(rest, " | ")
		shapes = append(shapes, Shape{Words: strings.Fields(words), Fields: fieldsOf(fields), Flags: flagsOf(flags)})
	}

	return shapes
}

// fieldsOf splits a comma list of fields. A dash is no fields.
func fieldsOf(text string) []string {
	if text == "-" {
		return nil
	}

	return strings.Split(text, ",")
}

// flagsOf reads a list of flags. A name that ends in an equal sign takes a value.
func flagsOf(text string) map[string]bool {
	flags := map[string]bool{}

	for _, flag := range strings.Fields(text) {
		name, takes := strings.CutSuffix(flag, "=")
		flags[name] = takes
	}

	return flags
}

// Check answers as the pinned gh does for the arguments that follow the word gh. It returns the refusal gh prints and false for an unknown field, an unknown flag, a flag with no
// value, or a command that is not in the table. A command that is not in the table is refused too, so a new gh call cannot skip the contract.
func Check(args []string) (string, bool) {
	for _, shape := range Shapes() {
		if len(args) >= len(shape.Words) && slices.Equal(args[:len(shape.Words)], shape.Words) {
			return shape.check(args[len(shape.Words):])
		}
	}

	return fmt.Sprintf("gh %s is not in the contract table of the pinned gh: add its fields and flags to gh-2.101.0.txt", strings.Join(args[:min(len(args), commandWords)], " ")), false
}

// check walks the arguments after the command words.
func (s Shape) check(rest []string) (string, bool) {
	for i := 0; i < len(rest); i++ {
		if len(rest[i]) < 2 || rest[i][0] != '-' {
			continue
		}

		taken, stderr := s.flag(rest[i:])
		if stderr != "" {
			return stderr, false
		}

		i += taken
	}

	return "", true
}

// flag checks the flag at the head of args. It returns how many of the next arguments the flag takes as its value, and gh's refusal, or an empty text.
func (s Shape) flag(args []string) (int, string) {
	name, value, inline := strings.Cut(args[0], "=")

	takes, known := s.Flags[name]

	switch {
	case !known:
		return 0, unknownFlag(name)
	case !takes:
		return 0, ""
	case inline:
		return 0, s.value(name, value)
	case len(args) == 1:
		return 0, "flag needs an argument: " + name
	default:
		return 1, s.value(name, args[1])
	}
}

// value checks the value of a flag. Only --json has a value that gh checks before it runs.
func (s Shape) value(name, value string) string {
	if name != "--json" {
		return ""
	}

	for _, field := range strings.Split(value, ",") {
		if !slices.Contains(s.Fields, field) {
			return fmt.Sprintf("Unknown JSON field: %q\nAvailable fields:\n  %s", field, strings.Join(s.Fields, "\n  "))
		}
	}

	return ""
}

// unknownFlag is the text of gh for a flag that the command does not have.
func unknownFlag(name string) string {
	if strings.HasPrefix(name, "--") {
		return "unknown flag: " + name
	}

	return fmt.Sprintf("unknown shorthand flag: '%s' in %s", name[1:2], name)
}
