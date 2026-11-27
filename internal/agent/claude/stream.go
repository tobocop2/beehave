package claude

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
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
	// rateLimitEvent is the line on which the CLI reports the state of the plan limits. limitRejected is its status when the provider refused the request.
	rateLimitEvent = "rate_limit_event"
	limitRejected  = "rejected"
	// rateLimitError is the error of an assistant line that the CLI writes when the provider refused the request at a limit.
	rateLimitError = "rate_limit"
	// overloadedError is the api_error of an assistant line that the CLI writes when the provider is overloaded. highLoad is the text of the line that the CLI
	// writes for a model under high load.
	overloadedError = "overloaded"
	highLoad        = "is experiencing high load"
	// creditsRequired is the errorCode of a rate_limit_info that asks for usage credits. overageWindow is the rateLimitType of the paid overage.
	creditsRequired = "credits_required"
	overageWindow   = "overage"
	// deniedPause and deniedCredits are the reasons of a refusal that no wait cures.
	deniedPause   = "the provider paused the usage of the organization, and no wait ends the pause: contact the support of the provider"
	deniedCredits = "the provider asks for usage credits, and no wait buys them: add usage credits or use another model"
)

// Calls reads one stream-json line.
// It returns the Bash calls of the line with their tool_use ids (at any depth), the Skill calls of an assistant line with their ids, the ids of the
// results of a user line that report no error, and the ids of the calls that a PreToolUse hook refused.
// A hook refusal is the tool_result of a call, with is_error set and a text that starts with "PreToolUse:"; the call never ran.
// It also returns the rate limit or spend limit that the line reports as the reason the provider refused the request, or the reason of a refusal that no wait cures.
// A line that is not valid JSON is Invalid and holds no call.
func (Adapter) Calls(line string) agent.Seen {
	if strings.TrimSpace(line) == "" {
		return agent.Seen{}
	}

	value, err := model.Decode[any](streamCommand, line) // the stream has no fixed shape
	if err != nil {
		return agent.Seen{Invalid: true}
	}

	limit, denied := refusalOf(value)

	return agent.Seen{
		Cost: costOf(value), Context: contextOf(value), Stop: stopOf(value), Failed: failedOf(value), Result: lineType(value) == resultType,
		Shell: toolCalls(value, bashTool, bashKey, false), Skills: skillCalls(value), Succeeded: succeededIDs(value), Refused: refusedIDs(value), Denials: denialsOf(value),
		Limit: limit, Denied: denied, Said: saidOf(value),
	}
}

// editTools are the file tools that write their target.
func editTools() []string { return []string{"Edit", "Write", "NotebookEdit"} }

// denialsOf returns the permission_denials of a result line: the tool of each refused call and its file_path, notebook_path or command.
func denialsOf(value any) []agent.Denial {
	if lineType(value) != resultType {
		return nil
	}

	line, _ := value.(map[string]any)             //nolint:errcheck // lineType found an object
	list, _ := line["permission_denials"].([]any) //nolint:errcheck // a missing or ill-typed list holds no denial

	var denials []agent.Denial

	for _, entry := range list {
		item, _ := entry.(map[string]any)     //nolint:errcheck // an entry that is not an object reads as empty
		tool, _ := item["tool_name"].(string) //nolint:errcheck // a missing name reads as ""
		input := object(item, "tool_input")
		path, _ := input["file_path"].(string)         //nolint:errcheck // a missing path reads as ""
		notebook, _ := input["notebook_path"].(string) //nolint:errcheck // a missing path reads as ""
		command, _ := input[bashKey].(string)          //nolint:errcheck // a missing command reads as ""

		if tool != "" {
			denials = append(denials, agent.Denial{Tool: tool, Target: cmp.Or(path, notebook, command), Edit: slices.Contains(editTools(), tool)})
		}
	}

	return denials
}

// refusalOf returns how a line says the provider refused more work: the limit that a wait cures, or the reason of a refusal that no wait cures.
// A line reports at most one of the two. A rate_limit_event reports one when its status is rejected and no paid overage serves the request.
// An assistant line with the error rate_limit reports one.
func refusalOf(value any) (*agent.Limit, string) {
	line, isObject := value.(map[string]any) // a line that is not an object reports no refusal
	if !isObject {
		return nil, ""
	}

	switch line["type"] {
	case rateLimitEvent:
		return rejected(object(line, "rate_limit_info"))
	case assistantType:
		if line["error"] == rateLimitError {
			return refusedAnswer(line)
		}
	}

	return nil, ""
}

// refusedAnswer returns the refusal of an assistant line with the error rate_limit. A pause of the organization (dailyCapPaused in the rate_limit_info of
// the api_error_params, which the CLI writes with the api_error usage_limit_reached) and an api_error that asks for usage credits are refusals that no wait
// cures. The pause is tested first, as the CLI does. The rate_limit_info has the shape of a rate_limit_event, so rejected reads it: the kind, the window and
// the reset time come from there. A line whose rate_limit_info is missing or does not reject is a rate limit with no window and no reset time.
func refusedAnswer(line map[string]any) (*agent.Limit, string) {
	info := object(object(line, "api_error_params"), "rate_limit_info")

	switch {
	case info["dailyCapPaused"] == true:
		return nil, deniedPause
	case asksForCredits(line["api_error"]):
		return nil, deniedCredits
	}

	if limit, reason := rejected(info); limit != nil || reason != "" {
		return limit, reason
	}

	if overloaded(line) {
		return &agent.Limit{Kind: agent.LimitOverload}, ""
	}

	return &agent.Limit{Kind: agent.LimitRate}, ""
}

// overloaded reports whether an assistant rate_limit line says that the provider is overloaded: its api_error is overloaded, or its text says that the model is under high load.
func overloaded(line map[string]any) bool {
	return line["api_error"] == overloadedError || strings.Contains(resultText(contentOf(line, assistantType)), highLoad)
}

// object returns the JSON object at a key of a JSON object, or nil when the value there is no object.
func object(parent map[string]any, key string) map[string]any {
	child, isObject := parent[key].(map[string]any) // the JSON shape is untyped
	if !isObject {
		return nil
	}

	return child
}

// asksForCredits reports whether the api_error of an assistant rate_limit line asks for usage credits.
func asksForCredits(apiError any) bool {
	switch apiError {
	case "long_context_credits_required", "model_requires_usage_credits":
		return true
	}

	return false
}

// deniedBy returns the reason why no wait cures the refusal of a rejected rate_limit_info, or "" when a wait cures it. The organization is paused
// (dailyCapPaused), or the request needs usage credits: errorCode is credits_required and rateLimitType is missing or overage, which is the test of the
// CLI. The pause is tested first, as the CLI does.
func deniedBy(info map[string]any) string {
	switch {
	case info["dailyCapPaused"] == true:
		return deniedPause
	case info["errorCode"] == creditsRequired && (info["rateLimitType"] == nil || info["rateLimitType"] == overageWindow):
		return deniedCredits
	}

	return ""
}

// rejected returns the refusal of the rate_limit_info of an event: nothing when the info does not refuse the request, the reason of a refusal that no
// wait cures (deniedBy), or else a limit.
// A rejected plan status with isUsingOverage true refuses nothing: paid overage serves the request. The limit is a spend limit only when paid overage
// was available and is now refused: overageStatus is rejected and overageDisabledReason says the credits or the spend cap ran out. The provider then
// takes work again at the earlier of the reset of the plan window and the reset of the spend. Any other reason, or none, means the account had no paid
// overage to refuse, so the limit is a rate limit with the plan reset time.
func rejected(info map[string]any) (*agent.Limit, string) {
	if info["status"] != limitRejected || info["isUsingOverage"] == true {
		return nil, ""
	}

	if reason := deniedBy(info); reason != "" {
		return nil, reason
	}

	limit := &agent.Limit{Kind: agent.LimitRate, ResetsAt: unixTime(info["resetsAt"])}
	if window, named := info["rateLimitType"].(string); named { // the JSON shape is untyped
		limit.Window = window
	}

	if info["overageStatus"] == limitRejected && spendRanOut(info["overageDisabledReason"]) {
		limit.Kind, limit.ResetsAt = agent.LimitSpend, earlier(limit.ResetsAt, unixTime(info["overageResetsAt"]))
	}

	return limit, ""
}

// spendRanOut reports whether the overageDisabledReason of a rate_limit_info says that paid overage was available and ran out: the account is out of
// credits, or it reached a spend cap. A pause of the organization carries the reason of the spend cap, and deniedBy takes it first. The CLI writes the reason org_spend_cap_reached as org_level_disabled_until on the stream.
func spendRanOut(reason any) bool {
	switch reason {
	case "out_of_credits", "org_level_disabled_until":
		return true
	}

	return false
}

// unixTime returns the time of a count of Unix seconds, or the zero time for a value that is no number above 0.
func unixTime(value any) time.Time {
	seconds, isNumber := value.(float64) // the JSON shape is untyped
	if !isNumber || seconds <= 0 {
		return time.Time{}
	}

	return time.Unix(int64(seconds), 0).UTC()
}

// earlier returns the earlier of two times. A zero time counts as no time.
func earlier(first, second time.Time) time.Time {
	if first.IsZero() || (!second.IsZero() && second.Before(first)) {
		return second
	}

	return first
}

// skillCalls returns each Skill call of an assistant line with its id, and the skill by its name without the prefix of the plugin of the engine.
func skillCalls(value any) []agent.Call {
	calls := toolCalls(value, skillTool, skillKey, true)
	for i := range calls {
		calls[i].Text = plainSkill(calls[i].Text)
	}

	return calls
}

// succeededIDs returns the tool_use ids of the tool_result blocks in a user line that report no error.
func succeededIDs(value any) []string {
	var ids []string

	for _, block := range contentOf(value, userType) {
		item, isBlock := block.(map[string]any) // each block is an object or holds no result
		id, _ := item["tool_use_id"].(string)   //nolint:errcheck // a missing id reads as ""
		if isBlock && item["type"] == toolResult && item["is_error"] != true && id != "" {
			ids = append(ids, id)
		}
	}

	return ids
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

// contextOf returns the tokens in the context of the main agent after the turn of an assistant line: input_tokens, cache_read_input_tokens and
// cache_creation_input_tokens of message.usage. A line of a sub-agent (parent_tool_use_id set), a line with no usage and any other line give 0.
func contextOf(value any) int64 {
	if lineType(value) != assistantType {
		return 0
	}

	line, _ := value.(map[string]any) //nolint:errcheck // lineType found an object
	if line["parent_tool_use_id"] != nil {
		return 0
	}

	usage := object(object(line, "message"), "usage")

	var total int64

	for _, field := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		count, _ := usage[field].(float64) //nolint:errcheck // a missing or ill-typed count reads as 0
		if count > 0 {
			total += int64(count)
		}
	}

	return total
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

// targetKeys are the input fields that name what a tool call acts on, in the order that saidOf tries them.
func targetKeys() []string {
	return []string{bashKey, "file_path", "notebook_path", "path", "pattern", "url", skillKey}
}

// saidOf returns the text blocks and the tool calls of an assistant line, in the order of the message.
func saidOf(value any) []agent.Said {
	var said []agent.Said

	for _, block := range contentOf(value, assistantType) {
		item, isBlock := block.(map[string]any) // each block is an object or not content
		if !isBlock {
			continue
		}

		switch item["type"] {
		case "text":
			if text, _ := item["text"].(string); text != "" { //nolint:errcheck // a missing text reads as ""
				said = append(said, agent.Said{Text: text})
			}
		case toolUseType:
			name, _ := item["name"].(string) //nolint:errcheck // a missing name reads as ""
			said = append(said, agent.Said{Call: true, Text: callText(name, object(item, "input"))})
		}
	}

	return said
}

// callText reads "<tool>: <target>" for a tool call, and the tool alone when the input names no target.
func callText(name string, input map[string]any) string {
	for _, key := range targetKeys() {
		if target, _ := input[key].(string); target != "" { //nolint:errcheck // a missing target reads as ""
			return name + ": " + target
		}
	}

	return name
}
