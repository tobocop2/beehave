package workflow

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/syntax"
)

// The values of the follow_ups argument of an agent call. With FollowUpsForbid, a review that leaves any finding open needs a commit.
const (
	FollowUpsAllow  = "allow"
	FollowUpsForbid = "forbid"
)

// FollowUpModes lists the values that follow_ups takes.
func FollowUpModes() []string { return []string{FollowUpsAllow, FollowUpsForbid} }

// followUpProblem says what is wrong with the follow_ups argument of an agent call in the text of a file, or returns an empty text.
// It reads only a string literal: a value that the check cannot see is left to the run.
func followUpProblem(value syntax.Expr) string {
	lit, ok := value.(*syntax.Literal)
	if !ok {
		return ""
	}

	mode, isText := lit.Value.(string)
	if isText && slices.Contains(FollowUpModes(), mode) {
		return ""
	}

	return fmt.Sprintf("follow_ups is %s. Use one of: %s.", lit.Raw, strings.Join(FollowUpModes(), ", "))
}

// arguments reports each wrong follow_ups argument of an agent call in the file, at the place of the value.
func (g *graph) arguments(parsed *syntax.File) {
	syntax.Walk(parsed, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok && isAgentCall(call) {
			g.followUps(call)
		}

		return true
	})
}

func isAgentCall(call *syntax.CallExpr) bool {
	fn, isName := call.Fn.(*syntax.Ident)

	return isName && fn.Name == "agent"
}

// followUps reports the follow_ups argument of one agent call when its value is a literal that is not listed,
// or is "forbid" and the literal outputs of the call lack the verdict. An outputs value that is not a list of literals is left to the run.
func (g *graph) followUps(call *syntax.CallExpr) {
	args := followUpArguments(call)
	value, outputs := args.value, args.outputs

	if value == nil {
		return
	}

	text := followUpProblem(value)
	if text == "" && isForbid(value) && !mayListVerdict(outputs) {
		text = fmt.Sprintf("follow_ups is %q, so outputs must list %q", FollowUpsForbid, "verdict")
	}

	if text != "" {
		g.report(placeOf(syntax.Start(value)), text)
	}
}

// followUpArguments returns the values of the keywords follow_ups and outputs of a call. An absent keyword gives nil.
func followUpArguments(call *syntax.CallExpr) (found followUpArgs) {
	for _, arg := range call.Args {
		pair, isPair := arg.(*syntax.BinaryExpr)
		if !isPair || pair.Op != syntax.EQ {
			continue
		}

		if key, isName := pair.X.(*syntax.Ident); isName {
			switch key.Name {
			case "follow_ups":
				found.value = pair.Y
			case "outputs":
				found.outputs = pair.Y
			}
		}
	}

	return found
}

// followUpArgs holds the follow_ups and outputs expressions of a call.
type followUpArgs struct{ value, outputs syntax.Expr }

// isForbid says whether the value is the literal "forbid".
func isForbid(value syntax.Expr) bool {
	lit, ok := value.(*syntax.Literal)

	return ok && lit.Value == FollowUpsForbid
}

// mayListVerdict says whether the outputs argument of a call can list the verdict. An absent argument and a list of literals without it cannot.
// A value that the check cannot read, or a list with an element that it cannot read, may.
func mayListVerdict(outputs syntax.Expr) bool {
	if outputs == nil {
		return false
	}

	list, isList := outputs.(*syntax.ListExpr)
	if !isList {
		return true
	}

	for _, element := range list.List {
		lit, isLit := element.(*syntax.Literal)
		if !isLit || lit.Value == "verdict" {
			return true
		}
	}

	return false
}
