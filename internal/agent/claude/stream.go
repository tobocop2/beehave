package claude

import (
	"cmp"
	"math"
	"sort"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	bashTool      = "Bash"
	bashKey       = "command"
	skillTool     = "Skill"
	skillKey      = "skill"
	assistantType = "assistant"
	userType      = "user"
	resultType    = "result"
	toolUseType   = "tool_use"
	toolResult    = "tool_result"
	hookRefusal   = "PreToolUse:"
	streamCommand = "the claude output"
)

// Calls reads one stream-json line.
// It returns the Bash calls of the line with their tool_use ids (at any depth), the Skill calls of an assistant line, and the ids of the calls that a PreToolUse hook refused.
// A hook refusal is the tool_result of a call, with is_error set and a text that starts with "PreToolUse:"; the call never ran.
// A line that is not valid JSON is Invalid and holds no call.
func (Adapter) Calls(line string) agent.Seen {
	if strings.TrimSpace(line) == "" {
		return agent.Seen{}
	}

	value, err := model.Decode[any](streamCommand, line) // the stream has no fixed shape
	if err != nil {
		return agent.Seen{Invalid: true}
	}

	return agent.Seen{
		Cost:    costOf(value),
		Stop:    stopOf(value),
		Failed:  failedOf(value),
		Shell:   toolCalls(value, bashTool, bashKey, false),
		Skills:  skillNames(value),
		Refused: refusedIDs(value),
		Result:  lineType(value) == resultType,
	}
}

// skillNames returns the skill of each Skill call of an assistant line.
func skillNames(value any) []string {
	var skills []string
	for _, call := range toolCalls(value, skillTool, skillKey, true) {
		skills = append(skills, call.Text)
	}

	return skills
}

// costOf returns the total_cost_usd of a result line, or 0 for any other line and for a cost that is no number of 0 or more.
func costOf(value any) float64 {
	if lineType(value) != resultType {
		return 0
	}

	line, _ := value.(map[string]any)           //nolint:errcheck // lineType found an object
	cost, _ := line["total_cost_usd"].(float64) //nolint:errcheck // a missing or ill-typed cost reads as 0

	if !(cost >= 0) || math.IsInf(cost, 0) { // the negated form also refuses NaN
		return 0
	}

	return cost
}

// stopOf returns the cap that a result line reports in its subtype, or "" for any other line.
func stopOf(value any) agent.Stop {
	if lineType(value) != resultType {
		return ""
	}

	line, _ := value.(map[string]any)      //nolint:errcheck // lineType found an object
	subtype, _ := line["subtype"].(string) //nolint:errcheck // a missing subtype reads as ""

	switch subtype {
	case "error_max_turns":
		return agent.StopTurns
	case "error_max_budget_usd":
		return agent.StopBudget
	}

	return ""
}

// failedOf returns the subtype of a result line that sets is_error and reports no cap, "unknown" when it has no subtype, or "" for any other line.
func failedOf(value any) string {
	if lineType(value) != resultType || stopOf(value) != "" {
		return ""
	}

	line, _ := value.(map[string]any)      //nolint:errcheck // lineType found an object
	subtype, _ := line["subtype"].(string) //nolint:errcheck // a missing subtype reads as ""

	if line["is_error"] != true {
		return ""
	}

	return cmp.Or(subtype, "unknown")
}

// lineType returns the type of a line, or "" when the line is not a JSON object.
func lineType(value any) string {
	line, _ := value.(map[string]any) //nolint:errcheck // a line that is not an object has no type
	name, _ := line["type"].(string)  //nolint:errcheck // a missing type reads as ""

	return name
}

// toolCalls returns the string at key in the input of each call to tool, with the id of the call, at any depth.
// With assistantOnly it reads only the tool_use blocks of an assistant line.
func toolCalls(value any, tool, key string, assistantOnly bool) []agent.Call {
	if assistantOnly {
		var found []agent.Call

		for _, block := range contentOf(value, assistantType) {
			if item, isBlock := block.(map[string]any); isBlock && item["type"] == toolUseType { // each block is an object or not a call
				found = append(found, walkCalls(item, tool, key)...)
			}
		}

		return found
	}

	return walkCalls(value, tool, key)
}

func walkCalls(value any, tool, key string) []agent.Call {
	var found []agent.Call

	switch node := value.(type) { // a decoded JSON value is one of these types
	case map[string]any:
		if input, ok := node["input"].(map[string]any); ok && node["name"] == tool { // the JSON shape is untyped
			if text, isText := input[key].(string); isText { // the field may hold any JSON type
				id, _ := node["id"].(string) //nolint:errcheck // a missing id reads as ""
				found = append(found, agent.Call{ID: id, Text: text})
			}
		}

		for _, name := range sortedKeys(node) {
			found = append(found, walkCalls(node[name], tool, key)...)
		}
	case []any:
		for _, child := range node {
			found = append(found, walkCalls(child, tool, key)...)
		}
	}

	return found
}

func sortedKeys(node map[string]any) []string {
	keys := make([]string, 0, len(node))
	for name := range node {
		keys = append(keys, name)
	}

	sort.Strings(keys)

	return keys
}

// contentOf returns the content blocks of the message of a line of the given type, or nil.
func contentOf(value any, kind string) []any {
	if lineType(value) != kind {
		return nil
	}

	line, _ := value.(map[string]any)              //nolint:errcheck // lineType proved an object
	message, _ := line["message"].(map[string]any) //nolint:errcheck // a missing message reads as no content
	content, _ := message["content"].([]any)       //nolint:errcheck // a missing list reads as no blocks

	return content
}

// refusedIDs returns the tool_use ids of the tool_result blocks in a user line that a PreToolUse hook refused.
func refusedIDs(value any) []string {
	var ids []string

	for _, block := range contentOf(value, userType) {
		item, isBlock := block.(map[string]any)
		if !isBlock || item["type"] != toolResult || item["is_error"] != true {
			continue
		}

		id, _ := item["tool_use_id"].(string) //nolint:errcheck // a missing id reads as ""
		if id != "" && strings.HasPrefix(resultText(item["content"]), hookRefusal) {
			ids = append(ids, id)
		}
	}

	return ids
}

// resultText returns the content of a tool_result as text: a string, or the first text block of a list.
func resultText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}

	list, _ := content.([]any) //nolint:errcheck // other types hold no text

	for _, block := range list {
		item, _ := block.(map[string]any) //nolint:errcheck // a block that is not an object holds no text
		if text, ok := item["text"].(string); ok {
			return text
		}
	}

	return ""
}
