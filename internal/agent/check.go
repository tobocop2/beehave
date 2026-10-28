package agent

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tobocop2/obee/internal/model"
)

const snippetRunes = 160

// Scanner finds the forbidden commands in one shell command.
type Scanner func(command string) []model.Finding

// CheckSkills counts the Skill calls for each required skill. It returns the lines to print and whether every skill was called.
func CheckSkills(path string, required []string, agentID string) ([]string, bool, error) {
	calls, err := SkillCalls(path, agentID)
	if err != nil {
		return nil, false, err
	}

	if len(required) == 0 {
		return []string{"no skill is required"}, true, nil
	}

	lines, missing := tally(calls, required)

	return append(lines, missing...), len(missing) == 0, nil
}

// tally returns a count line for each required skill, and a FAIL line for each one with no call.
func tally(calls []Call, required []string) ([]string, []string) {
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

// CheckTranscript returns one HIT line for each forbidden command in a Bash call of the transcript.
func CheckTranscript(path string, scan Scanner) ([]string, error) {
	calls, err := BashCalls(path)
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
