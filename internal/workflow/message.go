package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
)

// MaxMessageBytes is the largest JSON text that a message value may have.
const MaxMessageBytes = 64 << 10

// reservedPrefix starts the names that beehave uses for its own messages.
const reservedPrefix = "beehave."

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
	text, err := encoded(v)
	if err != nil {
		return nil, err
	}

	if len(text) > MaxMessageBytes {
		return nil, fmt.Errorf("the value is %d bytes of JSON, and the cap is %d. Send less data, or send the name of a file", len(text), MaxMessageBytes)
	}

	return json.RawMessage(text), nil
}
