package flow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/review"
	"github.com/tobocop2/obee/internal/workflow"
)

// outputVerdict and outputFindings are the outputs of a review call that the follow_ups option reads and sets.
// verdictChanges is the verdict that the prompts of a review use for a head that needs a commit.
const (
	outputVerdict  = "verdict"
	outputFindings = "findings"
	verdictChanges = "changes"
)

// findingClasses lists the classes that a finding may have.
func findingClasses() []review.Class {
	return []review.Class{review.Blocking, review.Deferred, review.PreExisting, review.Minor}
}

// followUps reads the follow_ups argument of an agent call: true when the workflow forbids follow-ups. A value that is not listed is an error.
// A call that forbids follow-ups must declare the output verdict, because the option sets it.
func followUps(mode string, declared []string) (forbid bool, err error) {
	if !slices.Contains(workflow.FollowUpModes(), mode) {
		return false, fmt.Errorf("follow_ups is %q. Use one of: %s", mode, strings.Join(workflow.FollowUpModes(), ", "))
	}

	forbid = mode == workflow.FollowUpsForbid
	if forbid && !slices.Contains(declared, outputVerdict) {
		return false, fmt.Errorf("follow_ups is %q, so outputs must list %q", mode, outputVerdict)
	}

	return forbid, nil
}

// applyFollowUps lets the policy of the workflow overrule the verdict. It reads the findings that the agent wrote, asks review.NeedsCommit
// and, when a finding needs a commit, sets the verdict to changes. When no finding needs a commit, the verdict stays as the agent wrote it.
// The result says what is wrong with the findings, so the caller can run the agent again.
func applyFollowUps(outputs map[string]any, written map[string]any) string {
	raw, ok := written[outputFindings]
	if !ok || raw == nil {
		return fmt.Sprintf("the agent did not write the output %q", outputFindings)
	}

	findings, problem := readFindings(raw)
	if problem != "" {
		return problem
	}

	if review.NeedsCommit(findings, review.Policy{NoFollowUps: true}) {
		outputs[outputVerdict] = verdictChanges
	}

	return ""
}

// readFindings converts the findings output into review findings. The second result says what is wrong with it.
func readFindings(raw any) ([]review.Finding, string) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Sprintf("the output %q must be a list", outputFindings)
	}

	found := make([]review.Finding, 0, len(list))

	for _, item := range list {
		one, problem := readFinding(item)
		if problem != "" {
			return nil, problem
		}

		found = append(found, one)
	}

	return found, ""
}

// readFinding converts one object of the findings output. The second result says what is wrong with it.
func readFinding(item any) (review.Finding, string) {
	object, isObject := item.(map[string]any)
	if !isObject {
		return review.Finding{}, "each finding must be an object with a class and a text"
	}

	class := review.Class(fmt.Sprint(object["class"]))
	if !slices.Contains(findingClasses(), class) {
		return review.Finding{}, fmt.Sprintf("the finding class %q is not one of: %s", class, joinClasses(findingClasses()))
	}

	return review.Finding{Class: class, Text: fmt.Sprint(object["text"])}, ""
}

func joinClasses(classes []review.Class) string {
	names := make([]string, len(classes))
	for i, class := range classes {
		names[i] = string(class)
	}

	return strings.Join(names, ", ")
}
