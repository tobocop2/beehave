package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// The functions of this file own the rules for the values of the arguments of a builtin. The run calls them with the value it got. The check calls
// them with the value of a literal, so a literal that the run refuses is a problem before the run starts.

// Optional reads an argument that is a list, a dict or another value of type T. None and an absent argument give the zero value of T.
// The second result is false when the argument has another type.
func Optional[T starlark.Value](v starlark.Value) (out T, ok bool) { //nolint:ireturn // the argument is any of the Starlark types
	if v == nil || v == starlark.None {
		return out, true
	}

	out, ok = v.(T)

	return out, ok
}

// Elements returns the members of a list, tuple or other sequence. The second result is false when v is not a sequence.
// None and an absent argument are an empty sequence.
func Elements(v starlark.Value) ([]starlark.Value, bool) {
	seq, ok := Optional[starlark.Sequence](v)
	if !ok || seq == nil {
		return nil, ok
	}

	out := make([]starlark.Value, 0, seq.Len())

	it := seq.Iterate()
	defer it.Done()

	for item := starlark.Value(nil); it.Next(&item); {
		out = append(out, item)
	}

	return out, true
}

// Words reads an argument that is a list of strings. None and an absent argument are empty.
func Words(name string, v starlark.Value) ([]string, error) {
	return ListOfEach(v, func(item starlark.Value) (string, error) {
		text, ok := starlark.AsString(item)
		if !ok {
			return "", fmt.Errorf("%s must hold strings only, and %s is a %s", name, item, item.Type())
		}

		return text, nil
	}, fmt.Errorf("%s must be a list of strings", name))
}

// ListOfEach reads an argument that is a list and converts each member with one. None and an absent argument give an empty list.
// A value that is not a list gives notList.
func ListOfEach[T any](v starlark.Value, one func(starlark.Value) (T, error), notList error) ([]T, error) {
	items, ok := Elements(v)
	if !ok {
		return nil, notList
	}

	var out []T

	for _, item := range items {
		value, err := one(item)
		if err != nil {
			return nil, err
		}

		out = append(out, value)
	}

	return out, nil
}

// Duration reads a timeout argument. An empty text is zero.
func Duration(name, text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a duration. Write a duration such as \"90s\" or \"10m\"", name, text)
	}

	return d, nil
}

// Deadline reads a timeout argument that the caller gave and that sets a deadline. A deadline of zero or less would mean no deadline, so it is refused.
func Deadline(name, text string) (time.Duration, error) {
	d, err := Duration(name, text)
	if err != nil {
		return 0, err
	}

	if d <= 0 {
		return 0, fmt.Errorf("%s is %q, which must be above zero. Write a duration such as \"48h\", or leave %s out to wait with no deadline", name, text, name)
	}

	return d, nil
}

// Budget refuses a cap in dollars that is negative, infinite or not a number. 0 means no cap.
func Budget(name string, dollars float64) error {
	if !(dollars >= 0) || math.IsInf(dollars, 0) { // the negated form also refuses NaN
		return fmt.Errorf("%s must be 0 (no cap) or more, in dollars, and a finite number", name)
	}

	return nil
}

// OutputTries refuses a number of tries below one.
func OutputTries(n int) error {
	if n < 1 {
		return errors.New("output_tries must be a whole number of 1 or more")
	}

	return nil
}

// ContextPercent refuses a context limit that is not a whole number of percent from 0 to 100. 0 means no limit.
func ContextPercent(n int) error {
	if n < 0 || n > 100 {
		return errors.New("max_context_percent must be a whole number from 0 (no limit) to 100")
	}

	return nil
}

// StopCodes reads the stop_on_rc list. Each code must be a whole number from 1 to 255.
func StopCodes(v starlark.Value) ([]int, error) {
	return ListOfEach(v, func(item starlark.Value) (int, error) {
		code, isInt := item.(starlark.Int)
		n, small := code.Int64()

		if !isInt || !small || !StopCode(int(n)) {
			return 0, errors.New("stop_on_rc must hold whole numbers from 1 to 255. Write each exit code as a number")
		}

		return int(n), nil
	}, errors.New("stop_on_rc must be a list of exit codes. Write stop_on_rc = [2]"))
}

// ProcessCap reads the max_new_processes keyword: a whole number of 1 or more, or zero for a call that sets none. A call with a host cannot set it,
// because a host job takes a command line only.
func ProcessCap(v starlark.Value, host string) (int, error) {
	if v == nil {
		return 0, nil
	}

	if host != "" {
		return 0, errors.New("max_new_processes cannot be used together with host. A host job takes a command line only. Remove max_new_processes or host")
	}

	n, isInt := v.(starlark.Int)
	limit, small := n.Int64()

	if !isInt || !small || limit < MinProcessCap || limit > MaxProcessCap {
		return 0, fmt.Errorf("max_new_processes must be a whole number from %d to %d. Write max_new_processes = 50", MinProcessCap, MaxProcessCap)
	}

	return int(limit), nil
}

// SandboxWith refuses the settings of a sandboxed command call that the sandbox cannot apply: a host job takes a command line only, and the sandbox gives
// the program no stdin and no process cap. Each setting is its zero value for a call that leaves it out.
func SandboxWith(host, stdin string, processes int) error {
	switch {
	case host != "":
		return errors.New("sandbox cannot be used together with host. A host job takes a command line only. Remove sandbox or host")
	case stdin != "":
		return errors.New("sandbox cannot be used together with stdin. The sandbox gives the program no input. Remove sandbox or stdin")
	case processes != 0:
		return errors.New("sandbox cannot be used together with max_new_processes. The sandbox sets no process cap. Remove sandbox or max_new_processes")
	}

	return nil
}

// holdKeyword is the keyword and the status name `hold`.
const holdKeyword = "hold"

// ClaimKeywords are the keywords that claim_stop in the workflow library takes and that preflight refuses.
func ClaimKeywords() []string { return []string{holdKeyword, "own", "own_draft"} }

// PreflightClaimRefusal is the sentence for a preflight call that passes a claim keyword. It names the library function that takes the keyword.
func PreflightClaimRefusal(keyword string) string {
	stop := "The claim stops"
	if keyword == "own_draft" {
		stop = "The draft stop"
	}

	return fmt.Sprintf("preflight takes no argument %s. %s moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass %s to it.",
		keyword, stop, keyword)
}

// BatchKeywords are the keywords that batch_merge in the workflow library takes and that merge refuses.
func BatchKeywords() []string { return []string{"prs", "gate_run", integrationKeyword} }

// MergeBatchRefusal is the sentence for a merge call that passes a keyword of the batch. It names the library function that takes the keyword.
func MergeBatchRefusal(keyword string) string {
	return fmt.Sprintf("merge takes no argument %s. The batch with its own gate moved to the library function batch_merge: load(\"@beehave//batch.star\", \"batch_merge\") and pass %s to it.",
		keyword, keyword)
}

// OwnDraft reads the own_draft field of a member of prs: the number of a pull request, 1 or more, or zero for a member that leaves it out.
func OwnDraft(v starlark.Value) (int, error) {
	if v == nil {
		return 0, nil
	}

	n, isInt := v.(starlark.Int)
	number, small := n.Int64()

	if !isInt || !small || number < 1 {
		return 0, errors.New("own_draft must be the number of a pull request, a whole number of 1 or more. Write own_draft = draft.number")
	}

	return int(number), nil
}

// SlotClass reads the slots keyword of a command call: the name of a slot class, or empty for a call that sets none. Only a host job takes a slot,
// so a call with no host cannot set it.
func SlotClass(v starlark.Value, host string) (string, error) {
	if v == nil {
		return "", nil
	}

	class, isText := starlark.AsString(v)
	if !isText || strings.TrimSpace(class) == "" {
		return "", errors.New("slots must be the name of a slot class. Write slots = \"heavy\"")
	}

	if host == "" {
		return "", errors.New("slots cannot be used without host. Only a host job takes a slot. Add host or remove slots")
	}

	return class, nil
}

// AllowList reads the list of globs in the doublestar dialect that the keyword of the given name holds. A leading `./` is dropped from each glob,
// because the paths that git lists have none. It returns the patterns as written and the clean globs. A missing argument gives none.
// The agent scope and the path check of before_review read their lists with it, so one dialect holds in both: an empty list, an empty entry,
// an absolute path, a path with a `..` part in any brace choice and a pattern that does not parse are refused, and each message names the pattern.
func AllowList(keyword string, v starlark.Value) (patterns, globs []string, err error) {
	patterns, err = Words(keyword, v)
	if err != nil {
		return nil, nil, err
	}

	if len(patterns) == 0 {
		if v == nil || v == starlark.None {
			return nil, nil, nil
		}

		return nil, nil, fmt.Errorf("%s is an empty list, which would allow every path. To turn the check off, leave the keyword out", keyword)
	}

	globs, err = cleanGlobs(keyword, patterns)

	return patterns, globs, err
}

// PathsOutsideAllow reads the allow keyword of paths_outside. It refuses None, because a check with no scope has nothing to compare, and reads the rest as AllowList does.
func PathsOutsideAllow(v starlark.Value) (globs []string, err error) {
	if v == starlark.None {
		return nil, fmt.Errorf("%s is None, which names no scope. To check no scope, leave out the call of %s", allowWord, pathsOutsideName)
	}

	_, globs, err = AllowList(allowWord, v)

	return globs, err
}

// cleanGlobs drops the leading `./` of each pattern and refuses a pattern that cannot match a path of the item folder, or that does not parse.
func cleanGlobs(keyword string, patterns []string) ([]string, error) {
	globs := make([]string, 0, len(patterns))

	for _, pattern := range patterns {
		clean := strings.TrimPrefix(pattern, "./")

		switch {
		case clean == "":
			return nil, fmt.Errorf("%s has the empty pattern %q. Write a glob such as src/**", keyword, pattern)
		case strings.HasPrefix(clean, "/"):
			return nil, fmt.Errorf("%s has the absolute pattern %q. A path is relative to the item folder", keyword, pattern)
		case slices.ContainsFunc(braceChoices(clean), leavesFolder):
			return nil, fmt.Errorf("%s has the pattern %q, which leaves the item folder", keyword, pattern)
		case !doublestar.ValidatePattern(clean):
			return nil, fmt.Errorf("%s has the pattern %q, which does not parse: %w", keyword, pattern, doublestar.ErrBadPattern)
		}

		globs = append(globs, clean)
	}

	return globs, nil
}

// leavesFolder reports whether one slash-separated part of the pattern is `..`.
func leavesFolder(pattern string) bool { return slices.Contains(strings.Split(pattern, "/"), "..") }

// braceChoices returns each pattern that the brace choices of the pattern spell, read as the doublestar matcher reads them.
// A pattern with no brace, or with a brace that does not close, is its own one choice.
func braceChoices(pattern string) []string {
	open := 0
	for open < len(pattern) && pattern[open] != '{' {
		open = tokenEnd(pattern, open) + 1
	}

	choices, closing := splitChoices(pattern, open+1)
	if closing < 0 {
		return []string{pattern}
	}

	var all []string
	for _, choice := range choices {
		all = append(all, braceChoices(pattern[:open]+choice+pattern[closing+1:])...)
	}

	return all
}

// splitChoices splits the brace choices that start at start at their top-level commas, and returns the index of the closing brace, or -1.
func splitChoices(s string, start int) (choices []string, closing int) {
	depth, from := 0, start
	for i := start; i < len(s); i = tokenEnd(s, i) + 1 {
		switch s[i] {
		case '{':
			depth++
		case ',':
			if depth == 0 {
				choices, from = append(choices, s[from:i]), i+1
			}
		case '}':
			if depth == 0 {
				return append(choices, s[from:i]), i
			}
			depth--
		}
	}

	return nil, -1
}

// tokenEnd returns the index of the last byte of the token at i: the byte that a backslash escapes, the end of a bracket class, or i itself.
func tokenEnd(s string, i int) int {
	switch s[i] {
	case '\\':
		return min(i+1, len(s)-1)
	case '[':
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				j++
			case ']':
				return j
			}
		}

		return len(s) - 1
	}

	return i
}

// AllowsPath reports whether one of the clean globs of AllowList matches the path. It judges nothing else: the agent scope and before_review both ask it.
func AllowsPath(globs []string, name string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return doublestar.MatchUnvalidated(g, name) })
}

// InputMap converts the inputs dict of an agent call into the data of its prompt template. An absent dict is empty.
func InputMap(v starlark.Value) (map[string]any, error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return nil, fmt.Errorf("inputs must be a dict, not a %s. Write inputs = {\"name\": value}", v.Type())
	}

	if dict == nil {
		return map[string]any{}, nil
	}

	return ToGo[map[string]any](dict)
}

// ToGo converts a value of a workflow file to a Go value for a prompt template or a request, through the encoder of the Starlark JSON library.
// A struct becomes a map. A number becomes an int64, or a float64 when it has a fraction, so that eq, gt and printf work on it.
// An integer beyond int64 stays a json.Number, which keeps its digits. A float that is NaN or infinite is refused by the encoder.
// T is any, or the type that the caller expects.
func ToGo[T any](v starlark.Value) (out T, err error) { //nolint:ireturn // T is any when the caller wants every kind of value
	decoded, err := encodeDecode(v)
	if err != nil {
		return out, err
	}

	settled := settle(decoded)

	out, ok := settled.(T)
	if !ok && settled != nil {
		err = fmt.Errorf("a %s is not the kind of value this place takes", v.Type())
	}

	return out, err
}

// DataRecord encodes the data dict of an agent call as the JSON text of its data file. An absent dict and None give "", which is no data file.
func DataRecord(v starlark.Value) (string, error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return "", fmt.Errorf("data must be a dict, not a %s. Write data = {\"name\": value}", v.Type())
	}

	if dict == nil {
		return "", nil
	}

	return encoded(dict)
}

// encoded is the text that the encoder of the Starlark JSON library gives for v, for a prompt or a request.
func encoded(v starlark.Value) (string, error) {
	text, err := jsonText(v)
	if err != nil {
		return "", fmt.Errorf("a value cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts (%w)", err)
	}

	return text, nil
}

// jsonText is the text that the encoder of the Starlark JSON library gives for v.
func jsonText(v starlark.Value) (string, error) {
	text, err := starlark.Call(&starlark.Thread{Name: "json"}, starjson.Module.Members["encode"], starlark.Tuple{v}, nil)
	if err != nil {
		return "", err
	}

	out, _ := starlark.AsString(text) // json.encode returns a string.

	return out, nil
}

// encodeDecode sends v through the encoder of the Starlark JSON library and decodes the text, with a number kept as a json.Number.
func encodeDecode(v starlark.Value) (decoded any, err error) {
	text, err := encoded(v)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()

	err = decoder.Decode(&decoded) // The text comes from the encoder, so it always decodes.

	return decoded, err
}

// settle walks a decoded value. A json.Number becomes an int64 or a float64.
// An integer beyond int64 is left as a json.Number.
func settle(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for key, item := range x {
			x[key] = settle(item)
		}
	case []any:
		for i, item := range x {
			x[i] = settle(item)
		}
	case json.Number:
		return settleNumber(x)
	}

	return v
}

// settleNumber returns an int64 for a whole number in range, a float64 for a number with a fraction or an exponent, and the number itself
// for an integer beyond int64.
func settleNumber(x json.Number) any {
	if n, err := x.Int64(); err == nil {
		return n
	}

	if f, err := x.Float64(); err == nil && strings.ContainsAny(string(x), ".eE") {
		return f
	}

	return x
}
