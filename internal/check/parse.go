package check

import (
	"io"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const (
	newline         = "\n"
	cmdSubstMarker  = "$(...)"
	quoteCharacters = "'\""
	// trailingOperators are the characters a line ends in when it continues on the next line.
	trailingOperators = " \t&|;"
)

// unit is one parsed piece of shell text with the text its offsets point into.
type unit struct {
	file *syntax.File
	src  string
}

// parseUnits parses the text. If it does not parse, each line that parses alone is a unit, and each line that does not is returned as unreadable.
func parseUnits(text string) ([]unit, []string) {
	if file, ok := parseText(text); ok {
		return []unit{{file, text}}, nil
	}

	var (
		units []unit
		bad   []string
	)

	for _, line := range strings.Split(text, newline) {
		lineUnits := unitsOfLine(line)
		units = append(units, lineUnits...)

		if len(lineUnits) != 1 || lineUnits[0].src != line {
			bad = append(bad, strings.TrimSpace(line))
		}
	}

	return units, bad
}

// unitsOfLine parses one line. A line that ends in an operator, such as `git add -A &&`, is also read without the operator, so its command is still judged.
func unitsOfLine(line string) []unit {
	if file, ok := parseText(line); ok {
		return []unit{{file, line}}
	}

	if open := strings.TrimRight(line, trailingOperators); open != line {
		if file, ok := parseText(open); ok {
			return []unit{{file, open}}
		}
	}

	return nil
}

func parseText(text string) (*syntax.File, bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")

	return file, err == nil
}

// srcOf returns the source text of a node.
func srcOf(src string, node syntax.Node) string {
	start, end := int(node.Pos().Offset()), int(node.End().Offset())
	return src[start:end]
}

// resolver is the variable environment of a scan: a wrapper variable has its value, any other name stays as `$name` text.
// With no scanner every name stays as text.
type resolver struct {
	s *scanner
}

func (r resolver) Get(name string) expand.Variable {
	if name == "IFS" {
		return expand.Variable{}
	}

	if r.s != nil {
		if value, ok := r.s.value(name); ok {
			return expand.Variable{Set: true, Kind: expand.String, Str: value}
		}
	}

	return expand.Variable{Set: true, Kind: expand.String, Str: "$" + name}
}

func (resolver) Each(func(string, expand.Variable) bool) {}

func (s *scanner) config() *expand.Config {
	return &expand.Config{
		Env:       resolver{s},
		CmdSubst:  cmdSubst,
		ProcSubst: procSubst,
	}
}

// words expands each argument to the words a shell would run; a wrapper variable splits into its own words.
func (s *scanner) words(u unit, args []*syntax.Word) []string {
	out, _ := s.namedWords(u, args)

	return out
}

// namedWords is words with a second list of the same length: for a word that is a lone variable whose value could not be read, the name of that variable, else "".
func (s *scanner) namedWords(u unit, args []*syntax.Word) ([]string, []string) {
	out := make([]string, 0, len(args))
	names := make([]string, 0, len(args))

	for _, arg := range args {
		name, isLone := lone(arg)
		if value, held := s.loneValue(name, isLone); held {
			split := splitValue(value)
			out = append(out, split...)
			names = append(names, make([]string, len(split))...)

			continue
		}

		out = append(out, s.literal(u, arg))
		names = append(names, name)
	}

	return out, names
}

// loneValue returns the wrapper value of a lone variable, or false when the word is not one or its value is unreadable.
func (s *scanner) loneValue(name string, isLone bool) (string, bool) {
	if !isLone {
		return "", false
	}

	return s.value(name)
}

// splitValue splits the value of a wrapper variable into the words a shell would run, so a quoted string stays one word.
// A value that is not one simple command splits at white space.
func splitValue(value string) []string {
	file, ok := parseText(value)
	if !ok || len(file.Stmts) != 1 {
		return strings.Fields(value)
	}

	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(file.Stmts[0].Redirs) > 0 || file.Stmts[0].Background {
		return strings.Fields(value)
	}

	u := unit{file, value}
	cfg := &expand.Config{Env: resolver{}, CmdSubst: cmdSubst, ProcSubst: procSubst}
	out := make([]string, 0, len(call.Args))

	for _, arg := range call.Args {
		out = append(out, expandWord(cfg, u, arg))
	}

	return out
}

func cmdSubst(out io.Writer, _ *syntax.CmdSubst) error {
	_, err := io.WriteString(out, cmdSubstMarker)

	return err
}

func procSubst(*syntax.ProcSubst) (string, error) { return cmdSubstMarker, nil }

// expandWord expands one word to one string; a word that expands to no word or to several, or that holds an operator expansion, stays as its source text.
func expandWord(cfg *expand.Config, u unit, word *syntax.Word) string {
	fields, err := expand.Fields(cfg, word)
	if err != nil || len(fields) != 1 || hasOperator(word) {
		return srcOf(u.src, word)
	}

	return fields[0]
}

// literal expands one word to one string; a word that expands to no word or to several, or that holds an operator expansion, stays as its source text.
func (s *scanner) literal(u unit, word *syntax.Word) string {
	return expandWord(s.config(), u, word)
}

// hasOperator reports whether the word holds an expansion with an operator, such as `${X:-.}`, whose value no static read can know.
func hasOperator(word *syntax.Word) bool {
	found := false

	syntax.Walk(word, func(node syntax.Node) bool {
		if expansion, ok := node.(*syntax.ParamExp); ok {
			found = found || expansion.Exp != nil || expansion.Slice != nil || expansion.Repl != nil || expansion.Length || expansion.Excl
		}

		return !found
	})

	return found
}

// lone returns the variable name when the word is only `$name`, `${name}` or one of them in double quotes.
func lone(word *syntax.Word) (string, bool) {
	parts := word.Parts
	if len(parts) == 1 {
		if quoted, ok := parts[0].(*syntax.DblQuoted); ok {
			parts = quoted.Parts
		}
	}

	if len(parts) != 1 {
		return "", false
	}

	expansion, ok := parts[0].(*syntax.ParamExp)
	if !ok || expansion.Param == nil || expansion.Exp != nil || expansion.Slice != nil || expansion.Repl != nil {
		return "", false
	}

	return expansion.Param.Value, true
}

// isQuoted reports whether the word has a quoted part.
func isQuoted(word *syntax.Word) bool {
	return slices.ContainsFunc(word.Parts, func(part syntax.WordPart) bool {
		switch part.(type) {
		case *syntax.DblQuoted, *syntax.SglQuoted:
			return true
		}

		return false
	})
}

// hasExpansion reports whether the word holds a parameter, command or arithmetic expansion that the shell runs.
func hasExpansion(word *syntax.Word) bool {
	found := false

	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp:
			found = true
		}

		return !found
	})

	return found
}

// valueText returns the text of an assigned value: the inside of a value that is one quoted string, else the source text.
func valueText(src string, word *syntax.Word) string {
	if len(word.Parts) == 1 {
		switch quoted := word.Parts[0].(type) {
		case *syntax.SglQuoted:
			return quoted.Value
		case *syntax.DblQuoted:
			if len(quoted.Parts) == 0 {
				return ""
			}

			return src[quoted.Parts[0].Pos().Offset():quoted.Parts[len(quoted.Parts)-1].End().Offset()]
		}
	}

	return strings.Trim(srcOf(src, word), quoteCharacters)
}
