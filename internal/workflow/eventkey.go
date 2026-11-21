package workflow

import (
	"fmt"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
)

const (
	publishName  = "publish"
	progressName = "progress"
	// reservedKeyPrefix starts the names of the events that the engine owns.
	reservedKeyPrefix = "beehave."
	keyKeyword        = "key"
)

// eventKeyPattern is the shape of the key of a published value.
var eventKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// CheckEventKey refuses a key that publish and progress cannot use: a key outside the shape, and a key of the engine.
func CheckEventKey(key string) error {
	switch {
	case strings.HasPrefix(key, reservedKeyPrefix):
		return fmt.Errorf("the key %q is reserved. Names that start with %s belong to the engine. Use another name", key, reservedKeyPrefix)
	case !eventKeyPattern.MatchString(key):
		return fmt.Errorf("the key %q is not valid. Use 1 to 64 characters from a-z, 0-9, _, . and -, and start with a letter or a digit", key)
	}

	return nil
}

// eventKeyRule is the rule of a key keyword.
func eventKeyRule(v starlark.Value) error {
	text, _ := starlark.AsString(v) // the check of the kind has refused a value that is not text

	return CheckEventKey(text)
}

// eventSignatures holds the builtins that publish a value of a run and read the values of its children.
func eventSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)
	key := Param{Name: keyKeyword, Kind: KindText, Required: true, Rule: eventKeyRule}

	return map[string][]Param{
		publishName:  {key, req("value", KindAny), label},
		progressName: {req("runs", KindAny), key, label},
	}
}
