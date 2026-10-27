package claude

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

const (
	bashTool            = "Bash"
	bashKey             = "command"
	skillTool           = "Skill"
	skillKey            = "skill"
	assistantType       = "assistant"
	toolUseType         = "tool_use"
	transcriptLineLimit = 256 << 20
	initialBuffer       = 64 << 10
	transcriptCommand   = "the transcript"
)

// BashCalls returns the command of each Bash call in a Claude Code transcript, at any depth of each line.
func (Adapter) BashCalls(path string) ([]model.Call, error) {
	return readCalls(path, func(value any) []string { return toolInputs(value, bashTool, bashKey) })
}

// SkillCalls returns the name of each Skill call an assistant line makes. With agentID, lines of other agents are skipped.
func (Adapter) SkillCalls(path, agentID string) ([]model.Call, error) {
	return readCalls(path, func(value any) []string { return skillNames(value, agentID) })
}

func readCalls(path string, pick func(any) []string) ([]model.Call, error) {
	file, err := os.Open(path) //nolint:gosec // the caller names the transcript path
	if err != nil {
		return nil, unreadable(path, err)
	}

	defer file.Close()

	calls, objects, err := scanCalls(file, pick)
	if err != nil {
		return nil, unreadable(path, err)
	}

	if objects == 0 {
		return nil, fmt.Errorf("%w %s: no line is a JSON object (check the path)", agent.ErrUnreadable, path)
	}

	return calls, nil
}

func scanCalls(source io.Reader, pick func(any) []string) ([]model.Call, int, error) {
	var calls []model.Call

	objects := 0

	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 0, initialBuffer), transcriptLineLimit)
	scanner.Split(splitLines)

	for number := 1; scanner.Scan(); number++ {
		found, isObject := callsOfLine(scanner.Bytes(), number, pick)
		calls = append(calls, found...)

		if isObject {
			objects++
		}
	}

	return calls, objects, scanner.Err()
}

// callsOfLine returns the calls on one line, and whether the line is a JSON object.
func callsOfLine(line []byte, number int, pick func(any) []string) ([]model.Call, bool) {
	value, err := model.Decode[any](transcriptCommand, string(line)) // the transcript has no fixed shape
	if err != nil {
		return nil, false
	}

	var calls []model.Call
	for _, text := range pick(value) {
		calls = append(calls, model.Call{Line: number, Text: text})
	}

	_, isObject := value.(map[string]any) // a transcript line is a JSON object

	return calls, isObject
}

func unreadable(path string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return fmt.Errorf("%w %s: %w (check the path)", agent.ErrUnreadable, path, err)
}

// splitLines ends a line at LF, CRLF or a lone CR.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	end := bytes.IndexAny(data, "\r\n")
	if end < 0 {
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}

		return 0, nil, nil
	}

	advance := end + 1
	if data[end] == '\r' {
		var more bool
		if advance, more = afterCR(data, advance, atEOF); more {
			return 0, nil, nil
		}
	}

	return advance, data[:end], nil
}

// afterCR returns where the next line starts after a CR, or true when the buffer must grow to tell a lone CR from CRLF.
func afterCR(data []byte, advance int, atEOF bool) (int, bool) {
	switch {
	case advance == len(data):
		return advance, !atEOF
	case data[advance] == '\n':
		return advance + 1, false
	}

	return advance, false
}

// toolInputs returns the string at key in the input of each call to tool, at any depth.
func toolInputs(value any, tool, key string) []string {
	var found []string

	switch node := value.(type) { // a decoded JSON value is one of these types
	case map[string]any:
		if input, ok := node["input"].(map[string]any); ok && node["name"] == tool { // the JSON shape is untyped
			if text, isText := input[key].(string); isText { // the field may hold any JSON type
				found = append(found, text)
			}
		}

		for _, name := range sortedKeys(node) {
			found = append(found, toolInputs(node[name], tool, key)...)
		}
	case []any:
		for _, child := range node {
			found = append(found, toolInputs(child, tool, key)...)
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

// skillNames returns the Skill names of the tool_use blocks in one assistant line.
func skillNames(value any, agentID string) []string {
	line, ok := value.(map[string]any) // the line is a JSON object or it holds no call
	if !ok || line["type"] != assistantType || (agentID != "" && line["agentId"] != agentID) {
		return nil
	}

	message, _ := line["message"].(map[string]any) //nolint:errcheck // a missing message reads as no content
	content, _ := message["content"].([]any)       //nolint:errcheck // a missing list reads as no blocks

	var names []string

	for _, item := range content {
		if block, isBlock := item.(map[string]any); isBlock && block["type"] == toolUseType { // each block is an object or not a call
			names = append(names, toolInputs(block, skillTool, skillKey)...)
		}
	}

	return names
}
