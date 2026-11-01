package workflow

import (
	"fmt"

	"go.starlark.net/syntax"
)

// The values of the follow_ups argument of an agent call. With FollowUpsForbid, a review that leaves any finding open needs a commit.
// With FollowUpsAudit, the call audits merged work: no commit follows, and the verdict is derived from the findings.
const (
	FollowUpsAllow  = "allow"
	FollowUpsForbid = "forbid"
	FollowUpsAudit  = "audit"
)

// FollowUpModes lists the values that follow_ups takes.
func FollowUpModes() []string { return []string{FollowUpsAllow, FollowUpsForbid, FollowUpsAudit} }

// The values of the kind argument of an agent call. A review step runs on the review queue, an implement step on the agents queue.
// A call with no kind is an implement step.
const (
	AgentKindImplement = "implement"
	AgentKindReview    = "review"
)

// AgentKinds lists the values that kind takes.
func AgentKinds() []string { return []string{AgentKindImplement, AgentKindReview} }

// The values of the lane argument of an agent call, and with LaneReview the kinds that the lane ledger of the method holds.
// A call with no lane is a fix lane, or a review lane when its kind is review.
const (
	LaneFix    = "fix"
	LaneRework = "rework"
	LaneRelay  = "relay"
	LaneReview = "review"
	LaneDesign = "design"
)

// Lanes lists the values that lane takes. A call with kind = "review" is a review lane without it, and also runs on the review queue.
func Lanes() []string { return []string{LaneFix, LaneRework, LaneRelay, LaneReview, LaneDesign} }

// LaneKind is the kind of the lane of an agent call in the ledger: review for the kind review, else the lane, else fix.
// The check of the call refuses a lane that the kind review contradicts.
func LaneKind(kind, lane string) string {
	if kind == AgentKindReview {
		return LaneReview
	}

	if lane == "" {
		return LaneFix
	}

	return lane
}

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

	if value == nil || mayListVerdict(outputs) {
		return
	}

	if mode, isMode := derivesVerdict(value); isMode {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("follow_ups is %q, so outputs must list %q", mode, "verdict"))
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

// derivesVerdict says whether the value is the literal "forbid" or "audit", the modes that set the output verdict, and returns the mode.
func derivesVerdict(value syntax.Expr) (string, bool) {
	lit, ok := value.(*syntax.Literal)
	if !ok {
		return "", false
	}

	mode, isText := lit.Value.(string)

	return mode, isText && (mode == FollowUpsForbid || mode == FollowUpsAudit)
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
