package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

// rawWindow is one window of the reply before it is checked.
type rawWindow struct {
	Utilization json.RawMessage `json:"utilization"`
	ResetsAt    any             `json:"resets_at"`
}

// answerIn returns the reading in one output line, and false when the line is not the answer to the request.
func answerIn(line []byte) (model.Reading, bool) {
	if !bytes.Contains(line, []byte(answerMark)) {
		return model.Reading{}, false
	}

	var message struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}

	if err := decode(line, &message); err != nil || message.Type != answerMark {
		return model.Reading{}, false
	}

	return replyIn(message.Response)
}

// replyIn returns the reading in the response of a control message, and false when it answers another request.
func replyIn(raw json.RawMessage) (model.Reading, bool) {
	var outer struct {
		RequestID string          `json:"request_id"`
		Subtype   string          `json:"subtype"`
		Error     any             `json:"error"`
		Response  json.RawMessage `json:"response"`
	}

	if err := decode(raw, &outer); err != nil || outer.RequestID != usageRequestID {
		return model.Reading{}, false
	}

	return readingOf(outer.Subtype, outer.Error, outer.Response), true
}

// ParseUsage returns the reading in the CLI output, or why there is none.
func ParseUsage(output string) model.Reading {
	for _, line := range strings.Split(output, "\n") {
		if reading, ok := answerIn([]byte(line)); ok {
			return reading
		}
	}

	return model.Unknown(noAnswer)
}

func readingOf(subtype string, failure any, body json.RawMessage) model.Reading {
	if subtype != "success" {
		return model.Unknown("the CLI refused get_usage: " + failureText(failure))
	}

	var inner struct {
		Available  *bool                      `json:"rate_limits_available"`
		RateLimits map[string]json.RawMessage `json:"rate_limits"`
	}

	if err := decode(body, &inner); err != nil {
		return model.Unknown("the reply is not an object")
	}

	return readingOfLimits(inner.Available, inner.RateLimits)
}

// readingOfLimits returns the reading of the rate_limits of a success reply.
// The CLI says available is false when plan limits do not apply: no login, an API key, Bedrock or Vertex.
func readingOfLimits(available *bool, limits map[string]json.RawMessage) model.Reading {
	if available != nil && !*available {
		return model.UnknownLogin(noPlanLimits)
	}

	if limits == nil {
		return model.Unknown("the reply has no rate_limits")
	}

	usage, reason := usageOf(limits)
	if reason != "" {
		return model.Unknown(reason)
	}

	return model.Known(usage)
}

func failureText(failure any) string {
	if failure == nil {
		return ""
	}

	return strings.TrimSpace(fmt.Sprint(failure))
}

// usageOf returns both windows of the reply, or the reason one is unusable.
func usageOf(limits map[string]json.RawMessage) (model.Usage, string) {
	five, reason := windowOf(limits["five_hour"], "five_hour")
	if reason != "" {
		return model.Usage{}, reason
	}

	seven, reason := windowOf(limits["seven_day"], "seven_day")
	if reason != "" {
		return model.Usage{}, reason
	}

	return model.Usage{FiveHour: five, SevenDay: seven}, ""
}

// windowOf returns a window of the reply, or the reason it is unusable.
// A window with the value null is a window that is not open now: 0 percent used and no reset time.
// A missing window, or one that is not an object, is unusable.
func windowOf(raw json.RawMessage, name string) (model.Window, string) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return model.Window{}, ""
	}

	var window rawWindow

	if err := decode(raw, &window); err != nil {
		return model.Window{}, fmt.Sprintf("the reply has no %s window", name)
	}

	percent, ok := number(window.Utilization)
	if !ok {
		return model.Window{}, fmt.Sprintf("the %s window has no utilization", name)
	}

	if percent < 0 || percent > maxPercent {
		return model.Window{}, fmt.Sprintf("the %s window has utilization out of range", name)
	}

	return model.Window{Percent: percent, ResetsAt: resetTime(window.ResetsAt)}, ""
}

// number returns the JSON number in raw. A number beyond the float range is infinite, so the range check refuses it.
func number(raw json.RawMessage) (float64, bool) {
	text := string(raw)
	if text == "" || !strings.ContainsAny(text[:1], "-0123456789") {
		return 0, false
	}

	// The text is a valid JSON number, so the only possible error is the range, and the value is then infinite.
	value, _ := strconv.ParseFloat(text, maxFloatBits) //nolint:errcheck // see above

	return value, true
}

// resetTime returns the time in value, or the zero time when value is not a time text. A time with no offset is UTC.
func resetTime(value any) time.Time {
	text, ok := value.(string)
	if !ok {
		return time.Time{}
	}

	if moment, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return moment
	}

	moment, err := time.ParseInLocation(resetLayout, text, time.UTC)
	if err != nil {
		return time.Time{}
	}

	return moment
}

// decode reads one JSON value of data into target. Data that is not an object of the target's shape is an error.
func decode(data []byte, target any) error {
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return errors.New("not an object")
	}

	return json.Unmarshal(data, target)
}
