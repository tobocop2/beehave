package shell

import (
	"path"
	"slices"

	"github.com/tobocop2/beehave/internal/model"
)

// Programs returns the base name of the command word of each command that the text runs, read past assignments, wrappers and shell strings.
// The second value is false when a command word cannot be read: a word that holds an expansion, a line that does not parse, or a command the scan reports as opaque.
func Programs(commandText string) ([]string, bool) {
	s := scanned(commandText, false, Protection{})

	hidden := slices.ContainsFunc(s.findings, func(finding model.Finding) bool {
		return finding.Kind == model.FindingUnparsed || finding.Kind == model.FindingOpaque
	})

	names := make([]string, 0, len(s.programs))

	for _, word := range s.programs {
		names = append(names, path.Base(word))
	}

	return names, !hidden && !slices.ContainsFunc(s.programs, unsettled)
}
