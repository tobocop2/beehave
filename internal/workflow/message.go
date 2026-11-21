package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
)

// MaxMessageBytes is the largest JSON text that a message value may have.
const MaxMessageBytes = 64 << 10

// reservedPrefix starts the names that beehave uses for its own messages.
const reservedPrefix = "beehave."

// The builtins of the inbox and their keyword of the topic.
const (
	recvName     = "recv"
	sendName     = "send"
	topicKeyword = "topic"
)

var messageName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// MessageName checks the topic or key of a message. what is "topic" or "key", and it names the argument in the error.
func MessageName(what, name string) error {
	if !messageName.MatchString(name) {
		return fmt.Errorf("%s %q is not allowed. Use 1 to 64 characters: lowercase letters, digits, _, . and -, and start with a letter or a digit", what, name)
	}

	if strings.HasPrefix(name, reservedPrefix) {
		return fmt.Errorf("%s %q is reserved for beehave. Use a name that does not start with %q", what, name, reservedPrefix)
	}

	return nil
}

// MessageValue encodes the value of a message as JSON and refuses a value that is not JSON or is over MaxMessageBytes.
func MessageValue(v starlark.Value) (json.RawMessage, error) {
	text, err := jsonText(v)
	if err != nil {
		return nil, fmt.Errorf("a message cannot hold this value. Use text, numbers, booleans, lists and dicts (%w)", err)
	}

	return MessageJSON([]byte(text))
}

// MessageJSON refuses the text of a message value that is not JSON or is over MaxMessageBytes.
func MessageJSON(text []byte) (json.RawMessage, error) {
	if !json.Valid(text) {
		return nil, errors.New("the value is not JSON. Give a JSON text, such as \"done\" or {\"n\": 1}")
	}

	if len(text) > MaxMessageBytes {
		return nil, fmt.Errorf("the value is %d bytes of JSON, and the cap is %d. Send less data, or send the name of a file", len(text), MaxMessageBytes)
	}

	return json.RawMessage(text), nil
}

// inboxResultFields holds the fields of the results of recv and send.
func inboxResultFields() map[string][]string {
	return map[string][]string{
		recvName: {"value", "sender", "id", "timed_out"},
		sendName: {"id"},
	}
}

// inboxSignatures holds the builtins that receive and send the messages of a run.
func inboxSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)
	topic := Param{Name: topicKeyword, Kind: KindText, Required: true, Rule: topicRule}

	return map[string][]Param{
		recvName: {topic, {Name: "timeout", Kind: KindText, Required: true, Rule: deadlineRule("timeout"), Why: "A receive waits at most this long, such as \"10m\"."}, label},
		sendName: {
			{Name: "to", Kind: KindAny, Required: true, Why: "Give \"parent\", or a run that spawn returned."},
			topic,
			{Name: "value", Kind: KindAny, Required: true, AsValue: true},
			label,
		},
	}
}

// topicsRule is the rule of the topics keyword of first_of.
func topicsRule(v starlark.Value) error {
	_, err := Topics(v)

	return err
}

// topicRule is the rule of the topic keyword of recv and send.
func topicRule(v starlark.Value) error {
	text, _ := starlark.AsString(v) // the unpack check has refused a value that is not text

	return MessageName(topicKeyword, text)
}
