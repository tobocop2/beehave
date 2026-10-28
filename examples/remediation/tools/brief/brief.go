// Package brief judges a brief before a lane starts: it admits a lane against the ledger, and it sizes the reading list of the brief.
// Every function here reads the text or the files it is given. None reads the environment or runs a command.
package brief

import (
	"fmt"
	"strings"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
)

const (
	// ReadLimit is the most bytes a lane reads before it starts: the brief and the files under its read-first list.
	ReadLimit = 160000
	// ContextCap is the default number of tokens a lane works in before it hands over.
	ContextCap = 150000
	// BytesPerToken turns a byte count into a token estimate.
	BytesPerToken = 4

	blanks = " \t\n\v\f\r"
)

func refuse(format string, args ...any) error {
	return &check.Refusal{Message: fmt.Sprintf(format, args...)}
}

// awkLines splits text at each newline and leaves no empty record after the last one.
func awkLines(text string) []string {
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}

	return parts
}
