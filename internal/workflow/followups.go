package workflow

import (
	"fmt"

	"go.starlark.net/syntax"
)

// The values of the follow_ups argument of an agent call. With FollowUpsForbid, a review that leaves any finding open needs a commit.
const (
	FollowUpsAllow  = "allow"
	FollowUpsForbid = "forbid"
)

// FollowUpModes lists the values that follow_ups takes.
func FollowUpModes() []string { return []string{FollowUpsAllow, FollowUpsForbid} }

// The values of the kind argument of an agent call. A review step runs on the review queue, an implement step on the agents queue.
// A call with no kind is an implement step.
const (
	AgentKindImplement = "implement"
	AgentKindReview    = "review"
)

// AgentKinds lists the values that kind takes.
func AgentKinds() []string { return []string{AgentKindImplement, AgentKindReview} }

// arguments reports each agent call in the file whose follow_ups is "forbid" and whose outputs lack the verdict, at the place of the value.
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

	return isName && fn.Name == agentName
}

// followUps reports the follow_ups argument of one agent call when it is "forbid" and the literal outputs of the call lack the verdict.
// An outputs value that is not a list of literals is left to the run. A value outside the listed modes is reported by the signature check.
func (g *graph) followUps(call *syntax.CallExpr) {
	args := followUpArguments(call)
	value, outputs := args.value, args.outputs

	if value != nil && isForbid(value) && !mayListVerdict(outputs) {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("follow_ups is %q, so outputs must list %q", FollowUpsForbid, "verdict"))
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
