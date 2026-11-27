package opencode

import (
	"cmp"
	"encoding/json"
	"strings"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	streamCommand = "the opencode output"
	textType      = "text"
	toolUseType   = "tool_use"
	stepFinish    = "step_finish"
	errorType     = "error"
	bashTool      = "bash"
	shellTool     = "shell"
	toolStep      = "tool-calls"
	errorStatus   = "error"
	refusal       = "beehest refused:"
)

// streamLine is one line of `opencode run --format json`.
type streamLine struct {
	Type      string  `json:"type"`
	SessionID string  `json:"sessionID"`
	Part      part    `json:"part"`
	Error     errBody `json:"error"`
}

type part struct {
	Text string `json:"text"`
	// CallID is the id of a call in 1.x. OpenCode 2.x has no such field and puts the call id in ID.
	CallID string   `json:"callID"`
	ID     string   `json:"id"`
	Tool   string   `json:"tool"`
	State  state    `json:"state"`
	Reason string   `json:"reason"`
	Cost   *float64 `json:"cost"`
	Tokens *tokens  `json:"tokens"`
}

type state struct {
	Status string `json:"status"`
	Input  struct {
		Command *string `json:"command"`
	} `json:"input"`
	Error string `json:"error"`
}

type tokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// errBody is the error of an error line: {"name":"APIError","data":{"message":"Not Found"}} in 1.19.5, and {"type":"unknown","message":"..."} in 2.2.5. A plain string reads as its message,
// so a version that writes the error in another shape still fails the run and does not make the line unreadable.
type errBody struct {
	Name    string
	Message string
}

// errObject is the object shape of an error: the 1.x shape has Name and Data, and the 2.x shape has Message.
type errObject struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
	Message string `json:"message"`
}

func (e *errBody) UnmarshalJSON(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		e.Message = text

		return nil
	}

	var object errObject

	if err := json.Unmarshal(raw, &object); err != nil {
		return nil //nolint:nilerr // an error of a shape that is not known still fails the run, as "unknown"
	}

	e.Name, e.Message = object.Name, cmp.Or(object.Data.Message, object.Message)

	return nil
}

// text returns the name and the message of the error, or "unknown" when it has neither.
func (e errBody) text() string {
	if e.Name != "" && e.Message != "" {
		return e.Name + ": " + e.Message
	}

	return cmp.Or(e.Name, e.Message, "unknown")
}

// Line reads the session id of any line and the text of a text line. The caller keeps the last text, which is the answer of the run.
func (Adapter) Line(line string) (string, string) {
	parsed, err := model.Decode[streamLine](streamCommand, line)
	if err != nil {
		return "", ""
	}

	if parsed.Type == textType {
		return parsed.SessionID, parsed.Part.Text
	}

	return parsed.SessionID, ""
}

// Calls reads one line of the output.
// A tool_use line of the bash tool is a shell call. OpenCode writes it when the call ends, so a call shows after it ran.
// A call whose error starts with "beehest refused:" is refused: the guard plugin threw before the call ran. It still counts as a shell call, with its id in Refused.
// A step_finish line carries the dollars (Spent) and the tokens (Usage) of its step. A step that ends for any reason but "tool-calls" ends the run (Result),
// because OpenCode has no result line. A step that ends with the reason "error", "length" or "content-filter" also sets Failed, as does an error line,
// because a failed run can exit with code 0.
// A line that is not valid JSON is Invalid.
func (Adapter) Calls(line string) agent.Seen {
	if strings.TrimSpace(line) == "" {
		return agent.Seen{}
	}

	parsed, err := model.Decode[streamLine](streamCommand, line)
	if err != nil {
		return agent.Seen{Invalid: true}
	}

	return seenOf(parsed)
}

// seenOf returns what one decoded line holds, by its type.
func seenOf(parsed streamLine) agent.Seen {
	switch parsed.Type {
	case textType:
		return agent.Seen{Said: textSaid(parsed.Part.Text)}
	case toolUseType:
		return toolSaid(parsed.Part)
	case stepFinish:
		return stepSeen(parsed.Part)
	case errorType:
		return agent.Seen{Failed: parsed.Error.text()}
	}

	return agent.Seen{}
}

// textSaid returns the message of a text line, and nothing for an empty one.
func textSaid(text string) []agent.Said {
	if text == "" {
		return nil
	}

	return []agent.Said{{Text: text}}
}

// toolSaid is the Seen of a tool_use line: its shell call, and the call it says.
func toolSaid(p part) agent.Seen {
	seen := toolSeen(p)
	seen.Said = []agent.Said{{Call: true, Text: callText(p)}}

	return seen
}

// callText reads "<tool>: <command>" for a tool call, and the tool alone when it has no command.
func callText(p part) string {
	if p.State.Input.Command == nil {
		return p.Tool
	}

	return p.Tool + ": " + *p.State.Input.Command
}

// toolSeen returns the shell call of a tool_use line, and its id in Refused when the guard plugin refused it.
func toolSeen(p part) agent.Seen {
	if (p.Tool != bashTool && p.Tool != shellTool) || p.State.Input.Command == nil {
		return agent.Seen{}
	}

	id := cmp.Or(p.CallID, p.ID)
	seen := agent.Seen{Shell: []agent.Call{{ID: id, Text: *p.State.Input.Command}}}

	if id != "" && p.State.Status == errorStatus && strings.HasPrefix(p.State.Error, refusal) {
		seen.Refused = []string{id}
	}

	return seen
}

// usageOf returns the tokens of a step. Input holds the cached tokens too, and Output the reasoning tokens.
func usageOf(t tokens) agent.Tokens {
	return agent.Tokens{
		Input:       t.Input + t.Cache.Read + t.Cache.Write,
		CachedInput: t.Cache.Read,
		Output:      t.Output + t.Reasoning,
		Reasoning:   t.Reasoning,
	}
}

// failedReason returns why a step that ended for the reason has no answer ("error", "length" or "content-filter": the model failed, ran out of room or was filtered),
// or "" for any other reason.
func failedReason(reason string) string {
	switch reason {
	case "error", "length", "content-filter":
		return "the step ended with the reason " + reason
	}

	return ""
}

// stepSeen returns the dollars, the tokens and the end of a step_finish line. A cost that is not a number of 0 or more adds nothing.
func stepSeen(p part) agent.Seen {
	seen := agent.Seen{Result: p.Reason != "" && p.Reason != toolStep, Failed: failedReason(p.Reason)}

	if p.Cost != nil {
		seen.Priced = true

		seen.Spent = max(*p.Cost, 0)
	}

	if t := p.Tokens; t != nil {
		seen.Usage = usageOf(*t)
	}

	return seen
}
