package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	xunicode "golang.org/x/text/encoding/unicode"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/internal/model"
)

const (
	// DefaultCap is the lane context cap in tokens when no argument sets one.
	DefaultCap    = 150000
	newline       = "\n"
	smallestPlain = 1e-4
	largestPlain  = 1e16

	hourStamp       = 13
	assistantRole   = "assistant"
	usageMarker     = `"usage"`
	assistantMarker = `"role":"assistant"`
	millions        = 1e6
	thousands       = 1e3
	overCap         = "OVER"
	underCap        = "under"
	noDescription   = "-"
)

var (
	laneNote    = regexp.MustCompile(`(?s)<task-id>([a-z0-9]+)</task-id>(.*?)<subagent_tokens>(\d+)</subagent_tokens>`)
	laneSummary = regexp.MustCompile(`<summary>Agent \\?"(.*?)\\?" [a-z]`)
	lineStamp   = regexp.MustCompile(`"timestamp":"([^"]+)"`)
)

type lane struct {
	tokens int64
	desc   string
	stamp  string
}

type hourCalls struct {
	calls         int64
	cache, output float64
}

// tally is a token count as the transcript wrote it: a whole number or a fraction.
type tally struct {
	value      float64
	fractional bool
}

func (t tally) plus(other tally) tally {
	return tally{t.value + other.value, t.fractional || other.fractional}
}

func (t tally) String() string {
	if !t.fractional {
		return strconv.FormatInt(int64(t.value), 10)
	}

	abs := math.Abs(t.value)
	if abs != 0 && (abs < smallestPlain || abs >= largestPlain) {
		return strconv.FormatFloat(t.value, 'e', -1, 64)
	}

	text := strconv.FormatFloat(t.value, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}

	return text
}

// usageEntry is the part of a transcript line that carries token counts.
type usageEntry struct {
	IsSidechain any
	Timestamp   string
	Message     struct {
		ID, Role string
		Usage    map[string]any
	}
}

// countOf returns the count under key; a missing key is zero. ok is false when the value is not a number.
func countOf(usage map[string]any, key string) (tally, bool) {
	raw, present := usage[key]
	if !present {
		return tally{}, true
	}

	number, isNumber := raw.(json.Number)
	if !isNumber {
		return tally{}, false
	}

	value, err := number.Float64()

	return tally{value, strings.ContainsAny(number.String(), ".eE")}, err == nil
}

// sumCounts adds the counts under the keys. ok is false when one is not a number.
func sumCounts(usage map[string]any, keys ...string) (tally, bool) {
	var sum tally

	for _, key := range keys {
		count, ok := countOf(usage, key)
		if !ok {
			return tally{}, false
		}

		sum = sum.plus(count)
	}

	return sum, true
}

// truthy reports whether a JSON value is true the way a script reads it: not null, false, zero, or empty.
func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		parsed, err := typed.Float64()

		return err != nil || parsed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}

	return value != nil
}

// spend is the orchestrator's calls by hour and the lanes that reported, in the order first seen.
type spend struct {
	own   map[string]*hourCalls
	lanes map[string]*lane
	order []string
}

// transcriptLines calls each for every line of the file. A line ends at LF or CR, so a CRLF pair also yields an empty line.
func transcriptLines(path string, each func(number int, line string)) error {
	file, err := os.Open(path) //nolint:gosec // the caller names the transcript path
	if err != nil {
		return err
	}

	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, math.MaxInt)
	scanner.Split(splitLine)

	for number := 0; scanner.Scan(); number++ {
		line := scanner.Text()
		if last := len(line) - 1; line[last] == '\r' || line[last] == '\n' {
			line = line[:last] + newline
		}

		each(number, replaceBadBytes(line))
	}

	return scanner.Err()
}

// splitLine ends a token after LF or CR and keeps that byte in the token. The last token may have no end byte.
func splitLine(data []byte, atEOF bool) (int, []byte, error) {
	if at := bytes.IndexAny(data, "\r\n"); at >= 0 {
		return at + 1, data[:at+1], nil
	}

	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}

	return 0, nil, nil
}

// replaceBadBytes puts one U+FFFD in the place of each run of bytes that is not valid UTF-8: a cut multi-byte sequence is one run, and any other bad byte is a run of its own.
func replaceBadBytes(line string) string {
	text, _ := xunicode.UTF8.NewDecoder().String(line) //nolint:errcheck // the decoder replaces bad bytes and never fails

	return text
}

// decodeUsage reads a line that holds exactly one JSON object. Keys match by exact case, and the last of a repeated key wins.
func decodeUsage(line string) (usageEntry, bool) {
	raw, ok := decodeObject(line)
	if !ok {
		return usageEntry{}, false
	}

	message, messageOK := raw["message"].(map[string]any)
	usage, usageOK := message["usage"].(map[string]any)

	if (!messageOK && truthy(raw["message"])) || (!usageOK && truthy(message["usage"])) {
		return usageEntry{}, false
	}

	entry := usageEntry{IsSidechain: raw["isSidechain"], Timestamp: stringOf(raw, "timestamp")}
	entry.Message.ID = stringOf(message, "id")
	entry.Message.Role = stringOf(message, "role")
	entry.Message.Usage = usage

	return entry, true
}

// decodeObject reads a line that holds one JSON object and nothing after it.
func decodeObject(line string) (map[string]any, bool) {
	var raw map[string]any

	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()

	if decoder.Decode(&raw) != nil || raw == nil || !errors.Is(decoder.Decode(new(json.RawMessage)), io.EOF) {
		return nil, false
	}

	return raw, true
}

// stringOf returns the string under the key, or an empty string when the value is absent or not a string.
func stringOf(object map[string]any, key string) string {
	if text, ok := object[key].(string); ok {
		return text
	}

	return ""
}

func (s *spend) recordCall(line string) {
	if !strings.Contains(line, usageMarker) || !strings.Contains(line, assistantMarker) {
		return
	}

	entry, ok := decodeUsage(line)
	if !ok || truthy(entry.IsSidechain) {
		return
	}

	cache, cacheOK := countOf(entry.Message.Usage, "cache_read_input_tokens")
	output, outputOK := countOf(entry.Message.Usage, "output_tokens")

	if cacheOK && outputOK {
		s.hour(firstRunes(entry.Timestamp, hourStamp)).add(cache.value, output.value)
	}
}

func (s *spend) hour(stamp string) *hourCalls {
	if s.own[stamp] == nil {
		s.own[stamp] = &hourCalls{}
	}

	return s.own[stamp]
}

func (h *hourCalls) add(cache, output float64) {
	h.calls++
	h.cache += cache
	h.output += output
}

func (s *spend) recordReports(line string) {
	if !strings.Contains(line, "subagent_tokens") {
		return
	}

	stamp := ""
	if found := lineStamp.FindStringSubmatch(line); found != nil {
		stamp = found[1]
	}

	for _, note := range laneNote.FindAllStringSubmatch(line, -1) {
		s.lane(note[1]).report(note[2], note[3], stamp)
	}
}

func (s *spend) lane(task string) *lane {
	if s.lanes[task] == nil {
		s.lanes[task] = &lane{}
		s.order = append(s.order, task)
	}

	return s.lanes[task]
}

// report records a task notification: the description, and the token count when it is the largest so far.
func (l *lane) report(body, count, stamp string) {
	if summary := laneSummary.FindStringSubmatch(body); summary != nil {
		l.desc = summary[1]
	}

	if tokens, err := strconv.ParseInt(count, 10, 64); err == nil && tokens > l.tokens {
		l.tokens, l.stamp = tokens, stamp
	}
}

func scanSpend(path string) (*spend, error) {
	found := &spend{own: map[string]*hourCalls{}, lanes: map[string]*lane{}}

	err := transcriptLines(path, func(_ int, line string) {
		found.recordCall(line)
		found.recordReports(line)
	})
	if err != nil {
		return nil, &check.Refusal{Message: fmt.Sprintf("cannot read the transcript %s: %s", path, reason(err))}
	}

	return found, nil
}

// reason returns the system message under a path error, with a capital first letter.
func reason(err error) string {
	var failed *fs.PathError
	if errors.As(err, &failed) {
		err = failed.Err
	}

	text := err.Error()
	first, width := utf8.DecodeRuneInString(text)

	return string(unicode.ToUpper(first)) + text[width:]
}

// findLane returns the task id for key: a task id, or a lane name the description holds as a whole word. The latest report wins.
func (s *spend) findLane(key string) (string, bool) {
	if s.lanes[key] != nil {
		return key, true
	}

	best, found := "", false
	word := func(char byte) bool { return isAlnum(char) || char == '_' || char == '-' }

	for _, task := range s.order {
		if holdsToken(s.lanes[task].desc, key, true, word) && (!found || s.lanes[task].stamp > s.lanes[best].stamp) {
			best, found = task, true
		}
	}

	return best, found
}

// LaneCap returns the lane context cap: the argument, else the default.
func LaneCap(arg *string) (int64, error) {
	raw := strconv.FormatInt(DefaultCap, 10)

	if arg != nil {
		raw = *arg
	}

	capTokens, err := strconv.ParseInt(raw, 10, 64)
	if errors.Is(err, strconv.ErrRange) && model.IsDigits(raw) {
		return math.MaxInt64, nil
	}

	if !model.IsDigits(raw) || err != nil || capTokens == 0 {
		return 0, &check.Refusal{Message: fmt.Sprintf("the lane context cap '%s' is not a whole number of tokens above zero.", raw)}
	}

	return capTokens, nil
}

// HoursTable returns the lines of the per-hour table: the orchestrator's calls and the lanes' tokens.
func HoursTable(path string, hours int) ([]string, error) {
	found, err := scanSpend(path)
	if err != nil {
		return nil, err
	}

	perHour, names := found.laneHours()
	for hour := range found.own {
		names[hour] = true
	}

	out := []string{"hour           calls  own_cache_read  own_output  lanes"}

	for _, hour := range lastHours(names, hours) {
		calls := found.hour(hour)
		out = append(out, fmt.Sprintf("%s  %5d  %12.1fM  %9.0fk  %5.1fM", hour, calls.calls, calls.cache/millions, calls.output/thousands, float64(perHour[hour])/millions))
	}

	return out, nil
}

// laneHours returns the tokens the lanes reported in each hour, and the hours.
func (s *spend) laneHours() (map[string]int64, map[string]bool) {
	perHour, names := map[string]int64{}, map[string]bool{}

	for _, current := range s.lanes {
		hour := firstRunes(current.stamp, hourStamp)
		perHour[hour] += current.tokens
		names[hour] = true
	}

	return perHour, names
}

// lastHours returns the names in order, the last n of them when n is above zero.
func lastHours(names map[string]bool, n int) []string {
	sorted := make([]string, 0, len(names))
	for hour := range names {
		sorted = append(sorted, hour)
	}

	sort.Strings(sorted)

	if n > 0 && len(sorted) > n {
		sorted = sorted[len(sorted)-n:]
	}

	return sorted
}

// LaneRows returns one row for each lane that reported, or for the one lane asked about, and whether that lane is over the cap.
func LaneRows(path string, capTokens int64, laneName *string) ([]string, bool, error) {
	found, err := scanSpend(path)
	if err != nil {
		return nil, false, err
	}

	chosen, err := found.chosen(path, laneName)
	if err != nil {
		return nil, false, err
	}

	rows := make([]string, 0, len(chosen))
	for _, task := range chosen {
		rows = append(rows, found.lanes[task].row(task, capTokens))
	}

	return rows, laneName != nil && found.lanes[chosen[0]].tokens > capTokens, nil
}

// chosen returns the task ids to show: the one lane named, or every lane in the order of its last report.
func (s *spend) chosen(path string, laneName *string) ([]string, error) {
	if laneName == nil {
		all := slices.Clone(s.order)
		slices.SortStableFunc(all, func(a, b string) int { return strings.Compare(s.lanes[a].stamp, s.lanes[b].stamp) })

		return all, nil
	}

	task, ok := s.findLane(*laneName)
	if !ok {
		return nil, &check.Refusal{Message: fmt.Sprintf("no lane '%s' has reported in %s, so its size is unknown, not under the cap. Give its task id.", *laneName, path)}
	}

	return []string{task}, nil
}

func (l *lane) row(task string, capTokens int64) string {
	verdict, desc := underCap, l.desc
	if l.tokens > capTokens {
		verdict = overCap
	}

	if desc == "" {
		desc = noDescription
	}

	return fmt.Sprintf("%s\t%s\t%d\t%s", task, desc, l.tokens, verdict)
}

// laneUsage is the spend of a lane, read from the lane's own transcript.
type laneUsage struct {
	calls          int64
	context, total tally
}

// callSpend is the context a call read and the total it read and wrote.
type callSpend struct {
	context, total tally
}

func spendOf(usage map[string]any) (callSpend, bool) {
	context, ok := sumCounts(usage, "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
	if !ok {
		return callSpend{}, false
	}

	output, ok := countOf(usage, "output_tokens")

	return callSpend{context, context.plus(output)}, ok
}

// usageCalls holds the calls of a lane transcript, in the order first seen, and the spend of each.
type usageCalls struct {
	seen map[string]callSpend
	keys []string
}

// add records the spend that a transcript line holds, if it holds one. A repeated message id replaces the earlier spend.
func (u *usageCalls) add(number int, line string) {
	key, spent, ok := spendLine(number, line)
	if !ok {
		return
	}

	if _, repeated := u.seen[key]; !repeated {
		u.keys = append(u.keys, key)
	}

	u.seen[key] = spent
}

// spendLine returns the message id and the spend of an assistant line that holds a usage object. A line with no id is named by its number.
func spendLine(number int, line string) (string, callSpend, bool) {
	if !strings.Contains(line, usageMarker) {
		return "", callSpend{}, false
	}

	entry, ok := decodeUsage(line)
	if !ok || entry.Message.Role != assistantRole || len(entry.Message.Usage) == 0 {
		return "", callSpend{}, false
	}

	spent, ok := spendOf(entry.Message.Usage)
	if entry.Message.ID == "" {
		return fmt.Sprintf("line-%d", number), spent, ok
	}

	return entry.Message.ID, spent, ok
}

// readLaneUsage returns the calls, the context of the last call and the total of a lane's transcript. ok is false when the file cannot be read or holds no usage.
func readLaneUsage(path string) (laneUsage, bool) {
	calls := &usageCalls{seen: map[string]callSpend{}}

	if err := transcriptLines(path, calls.add); err != nil || len(calls.keys) == 0 {
		return laneUsage{}, false
	}

	var total tally
	for _, key := range calls.keys {
		total = total.plus(calls.seen[key].total)
	}

	return laneUsage{int64(len(calls.keys)), calls.seen[calls.keys[len(calls.keys)-1]].context, total}, true
}

func readLaneTranscript(path string) (laneUsage, error) {
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return laneUsage{}, &check.Refusal{Message: "cannot read the lane transcript " + path}
	}

	usage, ok := readLaneUsage(path)
	if !ok {
		return laneUsage{}, &check.Refusal{Message: path + " holds no assistant message with a usage object, so the lane's spend is unknown."}
	}

	return usage, nil
}

// LaneContext returns the context of the last call of a lane's own transcript.
func LaneContext(path string) (string, error) {
	usage, err := readLaneTranscript(path)
	if err != nil {
		return "", err
	}

	return usage.context.String(), nil
}

// LaneTranscriptLine returns the calls, the context of the last call and the total of a lane's own transcript, as one line.
func LaneTranscriptLine(path string) (string, error) {
	usage, err := readLaneTranscript(path)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("calls=%d\tcontext=%s\ttotal=%s", usage.calls, usage.context, usage.total), nil
}
