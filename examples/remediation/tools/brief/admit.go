package brief

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/ledger"
)

const (
	// MinPatchRounds is the number of patch rounds on one mechanism after which only a design lane may go on.
	MinPatchRounds = 2
	// MinRegressions is the number of regressions in a handoff after which only a design lane may go on.
	MinRegressions = 2

	regressionsWord = "regressions="
	newDesignLane   = "dispatch a design lane next"
)

var notTokenChar = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// LaneKind is what an impl lane does to the pull request: a first fix, a rework after a review, or a relay of earlier work.
type LaneKind string

// The kinds of impl lane.
const (
	LaneFix    LaneKind = "fix"
	LaneRework LaneKind = "rework"
	LaneRelay  LaneKind = "relay"
)

// ParseLaneKind returns the lane kind that text names.
func ParseLaneKind(text string) (LaneKind, error) {
	for _, kind := range []LaneKind{LaneFix, LaneRework, LaneRelay} {
		if string(kind) == text {
			return kind, nil
		}
	}

	return "", refuse("the lane kind '%s' is not fix, rework or relay. Use one of them.", text)
}

// ledgerKind returns the kind of row that a lane of this kind writes.
func (k LaneKind) ledgerKind() ledger.Kind {
	switch k {
	case LaneRework:
		return ledger.KindRework
	case LaneRelay:
		return ledger.KindRelay
	case LaneFix:
	}

	return ledger.KindFix
}

// Lane is what a dispatch tells the brief about one lane. A zero value is an unset slot.
// Cap is the context cap in tokens, and zero means ContextCap.
type Lane struct {
	Key         string
	Kind        LaneKind
	Mechanism   string
	Acceptance  string
	BaseResult  string
	NewBehavior bool
	Handoff     string
	Transcript  string
	Cap         int
}

// Admission is what the ledger says about a lane before its brief is written: the kind of row to add and the mechanism it patches.
// A review lane has no kind, because a review row is keyed by its pull request.
type Admission struct {
	Kind      ledger.Kind
	Mechanism string
}

// Book is the part of the ledger that admission reads and the record writes.
type Book interface {
	Count(key, kinds string) (int, error)
	Rounds(key, mechanism string) (ledger.Rounds, error)
	Prior(key, lane string) (int, error)
	Add(request ledger.Request) (ledger.Added, error)
}

// HandoffCheck judges the handoff of the lane before this one. It returns the lines to show and whether the handoff is refused.
// A handoff that cannot be judged at all is the error.
type HandoffCheck func(text, name, transcript string, tokens int) (lines []string, refused bool, err error)

// Stopped is a brief that the rules hold back and that has lines for the orchestrator to follow. It is not a failure of the tool.
type Stopped struct {
	Lines []string
}

func (s *Stopped) Error() string {
	return strings.Join(s.Lines, "\n")
}

// Admit runs the gates that a lane of this kind passes and returns what its ledger row will say.
func Admit(kind Kind, name string, lane Lane, book Book, handoff HandoffCheck) (Admission, error) {
	if kind == KindRev {
		return Admission{Mechanism: lane.Mechanism}, nil
	}

	admission, err := admitLane(kind, name, lane, book)
	if err != nil {
		return Admission{}, err
	}

	prior, err := book.Prior(lane.Key, name)
	if err != nil || prior == 0 {
		return admission, err
	}

	return admission, readHandoff(kind, name, lane, book, handoff)
}

// admitLane checks that the lane has a key, and runs the gates of an impl lane.
func admitLane(kind Kind, name string, lane Lane, book Book) (Admission, error) {
	if lane.Key == "" {
		return Admission{}, refuse("the lane has no PR number; give one with the key field, or the item's name in the queue "+
			"before the pull request exists. The ledger counts the lanes of each pull request by it. Lane: %s.", name)
	}

	if kind != KindImpl {
		return Admission{Kind: ledger.Kind(kind), Mechanism: lane.Mechanism}, nil
	}

	ledgerKind, err := admitImpl(name, lane, book)

	return Admission{Kind: ledgerKind, Mechanism: lane.Mechanism}, err
}

// admitImpl runs the gates of an implementer lane and returns the kind of its row.
func admitImpl(name string, lane Lane, book Book) (ledger.Kind, error) {
	kind, err := ParseLaneKind(string(cmp.Or(lane.Kind, LaneFix)))
	if err != nil {
		return "", err
	}

	if err := checkAcceptance(name, lane); err != nil {
		return "", err
	}

	if err := needDesign(name, lane, book); err != nil {
		return "", err
	}

	if kind != LaneRelay {
		if err := admitMechanism(name, lane, book); err != nil {
			return "", err
		}
	}

	return kind.ledgerKind(), nil
}

func checkAcceptance(name string, lane Lane) error {
	if err := CheckSlot("acceptance", lane.Acceptance, name); err != nil {
		return err
	}

	if err := CheckSlot("base_result", lane.BaseResult, name); err != nil {
		return err
	}

	if !strings.HasPrefix(lane.Acceptance, noReason) && strings.HasPrefix(lane.BaseResult, noReason) {
		return refuse("the acceptance field names a script and the base_result field says none. " +
			"Run the script at the base first and give the path of that result with the base_result field.")
	}

	return nil
}

// needDesign stops a lane that adds new behavior when the ledger holds no design lane for its key.
func needDesign(name string, lane Lane, book Book) error {
	if !lane.NewBehavior {
		return nil
	}

	designs, err := book.Count(lane.Key, string(ledger.KindDesign))
	if err != nil || designs > 0 {
		return err
	}

	return &Stopped{Lines: []string{
		"REFUSED: lane " + name + " adds new behavior to " + lane.Key + ", and the ledger holds no design lane for it.",
		"         The first lane of new behavior is a design lane: " + newDesignLane + ". It writes",
		"         the rule and the acceptance script, runs the script at the base, and changes no product code.",
	}}
}

// admitMechanism checks that a fix or rework lane names its mechanism, and stops a third patch round.
func admitMechanism(name string, lane Lane, book Book) error {
	if lane.Mechanism == "" {
		return refuse("the lane %s has no mechanism; give one token with the mechanism field. It names the code path the lane patches, "+
			"such as robots-classifier. Reuse it verbatim for every later fix lane on that path.", name)
	}

	if notTokenChar.MatchString(lane.Mechanism) {
		return refuse("the mechanism '%s' is not one token. Use letters, digits, dot, dash and underscore.", lane.Mechanism)
	}

	rounds, err := book.Rounds(lane.Key, lane.Mechanism)
	if err != nil || rounds.Count < MinPatchRounds || rounds.Open {
		return err
	}

	return &Stopped{Lines: []string{
		"REFUSED: pull request " + lane.Key + " had " + strconv.Itoa(rounds.Count) + " patch rounds on the mechanism " + lane.Mechanism + ", with no design lane since.",
		"         A third patch round is a redesign that nobody wrote down. The next lane is a design lane:",
		"         " + newDesignLane + ", on the opus:stuck model tier by default. It writes the rule of the mechanism",
		"         and an acceptance script that covers every case the earlier rounds found.",
	}}
}

// readHandoff makes a successor start from a handoff that passes, and sends two regressions to a design lane.
func readHandoff(kind Kind, name string, lane Lane, book Book, handoff HandoffCheck) error {
	text, err := handoffText(name, lane)
	if err != nil {
		return err
	}

	lines, refused, err := handoff(text, lane.Handoff, lane.Transcript, cmp.Or(lane.Cap, ContextCap))
	if err != nil {
		return err
	}

	if refused {
		return &Stopped{Lines: lines}
	}

	return afterRegressions(kind, lane, book, lines)
}

// handoffText returns the text of the handoff of the lane. The transcript of the lane, when it exists, must be readable too.
func handoffText(name string, lane Lane) (string, error) {
	if lane.Handoff == "" {
		return "", refuse("the lane %s has no handoff, and the ledger already holds a fix lane for %s. "+
			"Give the path of the previous lane's handoff with the handoff field.", name, lane.Key)
	}

	if !isFile(lane.Handoff) {
		return "", refuse("no handoff at %s. Check the path of the previous lane's handoff.", lane.Handoff)
	}

	text, err := ReadText(lane.Handoff)
	if err != nil {
		return "", err
	}

	if lane.Transcript != "" && isFile(lane.Transcript) {
		_, err = ReadText(lane.Transcript)
	}

	return text, err
}

func afterRegressions(kind Kind, lane Lane, book Book, lines []string) error {
	regressions := regressionsIn(lines)
	if regressions < MinRegressions || kind == KindDesign {
		return nil
	}

	designs, err := book.Count(lane.Key, string(ledger.KindDesign))
	if err != nil || designs > 0 {
		return err
	}

	return &Stopped{Lines: []string{
		"REFUSED: " + lane.Handoff + " counts " + strconv.Itoa(regressions) + " regressions of pull request " + lane.Key + ", and the ledger holds no design lane for it.",
		"         The next brief is a design brief: " + newDesignLane + ". That lane writes the rule and",
		"         the acceptance script, runs the script at the base, and changes no product code.",
	}}
}

// wholeNumber returns the number that text spells, or zero when it spells none.
func wholeNumber(text string) int {
	number, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}

	return number
}

// regressionsIn returns the number after the last `regressions=` word of the lines, or zero.
func regressionsIn(lines []string) int {
	found := 0

	for _, line := range lines {
		for _, word := range strings.Fields(line) {
			if value, ok := strings.CutPrefix(word, regressionsWord); ok {
				found = wholeNumber(value)
			}
		}
	}

	return found
}
