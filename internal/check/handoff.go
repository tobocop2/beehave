package check

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const (
	regressionsLabel = "Regressions of this pull request so far"
	tokensLabel      = "Tokens at handoff"
	jobNotReadLabel  = "Job not read because"
	launchedMarker   = "launched:"
)

var handoffBullet = regexp.MustCompile(`^[-*] +`)

// HandoffOptions are the optional inputs of the handoff check: the lane's own transcript and the context cap as text.
type HandoffOptions struct {
	Transcript string
	Cap        *string
}

// HandoffResult holds the lines to print and whether the handoff is refused.
type HandoffResult struct {
	Lines   []string
	Refused bool
}

func (r *HandoffResult) refuse(lines ...string) {
	r.Lines = append(r.Lines, lines...)
	r.Refused = true
}

// valueOf returns the text after the first `<label>:` at the start of a line, a list dash allowed.
func valueOf(text, label string) string {
	for _, line := range strings.Split(text, newline) {
		if rest, ok := strings.CutPrefix(handoffBullet.ReplaceAllString(line, ""), label+":"); ok {
			return strings.Trim(rest, " ")
		}
	}

	return ""
}

// handoffTokens returns the tokens of the lane: the handoff line, else the lane transcript, else empty.
func handoffTokens(text, name string, opts HandoffOptions, result *HandoffResult) (string, error) {
	first, _, _ := strings.Cut(valueOf(text, tokensLabel), " ")
	if model.IsDigits(first) {
		return first, nil
	}

	if opts.Transcript == "" {
		result.refuse(
			fmt.Sprintf("REFUSED: the spend of this lane is unknown: %s has no line 'Tokens at handoff: <n>'. Add the line, or pass", name),
			"         --transcript <the lane's own transcript .jsonl>. An unknown spend is not a small one.",
		)

		return "", nil
	}

	usage, err := readLaneTranscript(opts.Transcript)
	if err != nil {
		return "", &Refusal{Message: err.Error(), Lines: result.Lines}
	}

	return usage.context.String(), nil
}

// Handoff checks the handoff text. name is how the handoff is called in messages.
func Handoff(text, name string, opts HandoffOptions) (HandoffResult, error) {
	capText, err := capOf(opts.Cap)
	if err != nil {
		return HandoffResult{}, err
	}

	var result HandoffResult

	checkRegressions(text, name, &result)

	tokens, err := handoffTokens(text, name, opts, &result)
	if err != nil {
		return result, err
	}

	result.conclude(text, name, tokens, capText)

	return result, nil
}

// conclude refuses a handoff written between a launch and its read, and says OK for one that nothing refused.
func (r *HandoffResult) conclude(text, name, tokens, capText string) {
	if launchedUnread(text) {
		r.refuse(launchedLines(name, tokens, capText)...)
	}

	if !r.Refused {
		r.Lines = append(r.Lines, fmt.Sprintf("OK: regressions=%s tokens=%s", valueOf(text, regressionsLabel), tokens))
	}
}

func launchedUnread(text string) bool {
	return strings.Contains(text, launchedMarker) && valueOf(text, jobNotReadLabel) == ""
}

// capOf returns the cap as text: the default when none is given, a refusal when it is not a whole number above zero.
func capOf(given *string) (string, error) {
	if given == nil {
		return strconv.Itoa(defaultCap), nil
	}

	if !model.IsDigits(*given) || *given == "0" {
		return "", &Refusal{Message: fmt.Sprintf("the lane context cap '%s' is not a whole number of tokens above zero.", *given)}
	}

	return *given, nil
}

func checkRegressions(text, name string, result *HandoffResult) {
	regressions := valueOf(text, regressionsLabel)

	switch {
	case regressions == "":
		result.refuse(
			fmt.Sprintf("REFUSED: %s has no line 'Regressions of this pull request so far: <n>'. Add it. A regression is a defect an", name),
			"         earlier lane of the same pull request introduced; write 0 when there is none.",
		)
	case !model.IsDigits(regressions):
		result.refuse(fmt.Sprintf("REFUSED: the regression count '%s' in %s is not a whole number.", regressions, name))
	}
}

func launchedLines(name, tokens, capText string) []string {
	over, shown := "", tokens

	if model.IsDigits(tokens) && overCapText(tokens, capText) {
		over = ", over the cap of " + capText
	}

	if tokens == "" {
		shown = "an unknown number of"
	}

	return []string{
		fmt.Sprintf("REFUSED: %s holds 'launched:' and the lane is at %s tokens%s. A lane hands off", name, shown, over),
		"         after a job it has read, never between a launch and its read. Read the job, or add the line",
		"         'Job not read because: <reason>'. A lane near the cap launches no job: the orchestrator",
		"         launches it with its waiter.",
	}
}

// overCapText compares two whole numbers written as text, so a count past the integer range still compares.
func overCapText(tokens, capText string) bool {
	tokens, capText = strings.TrimLeft(tokens, "0"), strings.TrimLeft(capText, "0")
	if len(tokens) != len(capText) {
		return len(tokens) > len(capText)
	}

	return tokens > capText
}
