package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// The keywords of a wait that name who answers and what an answer may be.
const (
	fromKeyword    = "to"
	quorumKeyword  = "quorum"
	choicesKeyword = "choices"
)

// Names reads a list of names that are each text, with no empty name, no name with spaces around it and no name twice.
// The keyword is one of `to` and `choices`, and it names the list in the error.
func Names(keyword string, v starlark.Value) ([]string, error) {
	names, err := Words(keyword, v)
	if err != nil {
		return nil, err
	}

	return names, CheckNames(keyword, names)
}

// OptionalNames is Names for a keyword that the call may leave out. An absent keyword gives no names.
func OptionalNames(keyword string, v starlark.Value) ([]string, error) {
	if v == nil {
		return nil, nil
	}

	return Names(keyword, v)
}

// CheckNames refuses a list of names that is empty, holds an empty name or a name with spaces around it, or holds a name twice.
func CheckNames(keyword string, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s is an empty list. Leave the argument out, or name at least one", keyword)
	}

	for i, name := range names {
		switch {
		case name == "" || name != strings.TrimSpace(name):
			return fmt.Errorf("%s holds %q, which is empty or has spaces around it", keyword, name)
		case slices.Contains(names[:i], name):
			return fmt.Errorf("%s holds %q twice", keyword, name)
		}
	}

	return nil
}

// Quorum reads the quorum of a wait: a whole number of 1 or more.
func Quorum(v starlark.Value) (int, error) {
	count, ok := v.(starlark.Int)
	if !ok {
		return 0, errors.New("quorum must be a whole number of 1 or more")
	}

	n, ok := count.Int64()
	if !ok || n < 1 || n > maxQuorum {
		return 0, errors.New("quorum must be a whole number of 1 or more")
	}

	return int(n), nil
}

// maxQuorum keeps a quorum that no list of names can reach from overflowing the check against the length of the list.
const maxQuorum = 1 << 20

// CheckQuorum refuses a quorum that no answer from the named people can reach. A quorum of 0 means the call set none.
func CheckQuorum(quorum int, from []string) error {
	switch {
	case quorum == 0:
		return nil
	case len(from) == 0:
		return errors.New("quorum needs `to`, the list of people who answer. Add to = [...] to the call, or leave quorum out")
	case quorum > len(from):
		return fmt.Errorf("quorum is %d, and `to` names %d people. Lower the quorum, or name more people", quorum, len(from))
	}

	return nil
}

// waitParams are the keywords of a wait: the question, a deadline, the people who answer, how many of them must, the texts an answer may be, and the name.
func waitParams() []Param {
	return []Param{
		opt("question", KindText), ruled("timeout", KindText, deadlineRule("timeout")),
		ruled(fromKeyword, KindList, fromRule), ruled(quorumKeyword, KindInt, quorumRule), ruled(choicesKeyword, KindList, choicesRule),
		opt(nameKeyword, KindText),
	}
}

func fromRule(v starlark.Value) error {
	_, err := Names(fromKeyword, v)

	return err
}

func choicesRule(v starlark.Value) error {
	_, err := Names(choicesKeyword, v)

	return err
}

func quorumRule(v starlark.Value) error {
	_, err := Quorum(v)

	return err
}

// asking checks the rule of a wait that ties quorum to from. The check reads the lists that are literals of text, and leaves the rest to the run.
func (g *graph) asking(call *syntax.CallExpr, params []Param) {
	index := func(name string) int {
		return slices.IndexFunc(params, func(p Param) bool { return p.Name == name })
	}

	arg := argument(call, quorumKeyword, index(quorumKeyword))
	quorum, ok := literalQuorum(arg)

	fromArg := argument(call, fromKeyword, index(fromKeyword))
	from, known := textList(fromArg)

	// An empty list is reported by the rule of its keyword.
	if !ok || !known || (fromArg != nil && len(from) == 0) {
		return
	}

	if err := CheckQuorum(quorum, from); err != nil {
		g.report(placeOf(syntax.Start(arg)), waitName+": "+err.Error()+".")
	}
}

// literalQuorum is the quorum of an argument that is a literal whole number of 1 or more. Any other value is left to the rule of the keyword and to the run.
func literalQuorum(arg syntax.Expr) (int, bool) {
	if arg == nil {
		return 0, false
	}

	value, isLiteral := literalValue(arg)
	if !isLiteral {
		return 0, false
	}

	quorum, err := Quorum(value)

	return quorum, err == nil
}
