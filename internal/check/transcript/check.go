package transcript

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tobocop2/obee/internal/model"
)

const snippetRunes = 160

// scanner finds the forbidden commands in one shell command.
type scanner func(command string) []model.Finding

// Skills counts the Skill calls for each required skill. It returns the lines to print and whether every skill was called. The reader returns the Skill calls of the transcript at path.
func Skills(read func(path, agentID string) ([]model.Call, error), path string, required []string, agentID string) ([]string, bool, error) {
	calls, err := read(path, agentID)
	if err != nil {
		return nil, false, err
	}

	if len(required) == 0 {
		return []string{"no skill is required"}, true, nil
	}

	lines, missing := countSkills(calls, required)

	return append(lines, missing...), len(missing) == 0, nil
}

// countSkills returns a count line for each required skill, and a FAIL line for each one with no call.
func countSkills(calls []model.Call, required []string) ([]string, []string) {
	called := map[string]int{}
	for _, call := range calls {
		called[call.Text]++
	}

	lines, missing := make([]string, 0, len(required)), []string{}

	for _, name := range required {
		lines = append(lines, fmt.Sprintf("%s: %d", name, called[name]))

		if called[name] == 0 {
			missing = append(missing, "FAIL: no Skill call for "+name)
		}
	}

	return lines, missing
}

// Bash returns one HIT line for each forbidden command in a Bash call of the transcript. The reader returns the Bash calls of the transcript at path.
func Bash(read func(path string) ([]model.Call, error), path string, scan scanner) ([]string, error) {
	calls, err := read(path)
	if err != nil {
		return nil, err
	}

	var hits []string

	for _, call := range calls {
		for _, finding := range scan(call.Text) {
			hits = append(hits, fmt.Sprintf("HIT line %d: %s: %s", call.Line, finding.Kind, snippet(finding.Text)))
		}
	}

	return hits, nil
}

func snippet(text string) string {
	collapsed := []rune(strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " "))
	if len(collapsed) > snippetRunes {
		collapsed = collapsed[:snippetRunes]
	}

	return string(collapsed)
}
