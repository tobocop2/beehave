package workflow

import (
	"cmp"
	"fmt"
	"slices"
)

// OnOpenKeyword is the keyword of a spawn call, and the name of the start option of beehest run, that says what a start does with an open run of the item.
const OnOpenKeyword = "on_open"

// OnOpen is what a start does with the open run of its item.
type OnOpen string

const (
	// OnOpenJoin joins the open run and drops the input of the later start.
	OnOpenJoin OnOpen = "join"
	// OnOpenReplace cancels the open run with its children and starts the next attempt.
	OnOpenReplace OnOpen = "replace"
)

// OnOpenModes lists the values of the on_open option of a start.
func OnOpenModes() []string { return []string{string(OnOpenJoin), string(OnOpenReplace)} }

// OpenPolicy reads the on_open word of a start. An empty word is join.
func OpenPolicy(text string) (OnOpen, error) {
	mode := cmp.Or(OnOpen(text), OnOpenJoin)
	if !slices.Contains(OnOpenModes(), string(mode)) {
		return "", fmt.Errorf("%s is %q. Use \"join\" or \"replace\"", OnOpenKeyword, text)
	}

	return mode, nil
}
