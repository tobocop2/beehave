package brief

import (
	"cmp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/ledger"
)

// Request is one brief to write. RulesPath is the rules file of the project, and SlotsFile may be empty.
// Model is the model of the dispatch, written `name` or `name:row`. At is the time the ledger row records.
type Request struct {
	Kind      Kind
	Name      string
	Model     string
	RulesPath string
	SlotsFile string
	Lane      Lane
	At        time.Time
}

// Deps are what a brief needs from outside: the ledger and the check of a handoff.
type Deps struct {
	Book    Book
	Handoff HandoffCheck
}

// Generated is a written brief and the lines the ledger reported for the lane.
type Generated struct {
	Text   string
	Ledger []string
}

// Generate writes the brief of a lane and adds the row of the lane to the ledger.
// A refused brief writes no row. A brief the rules hold back is a *Stopped error.
func Generate(request Request, deps Deps) (Generated, error) {
	spec, slots, err := specOf(request)
	if err != nil {
		return Generated{}, err
	}

	admission, err := Admit(request.Kind, request.Name, request.Lane, deps.Book, deps.Handoff)
	if err != nil {
		return Generated{}, err
	}

	text, err := write(spec, slots)
	if err != nil {
		return Generated{}, err
	}

	lines, err := record(request, admission, deps.Book)

	return Generated{Text: text, Ledger: lines}, err
}

// write returns the filled text of the brief, and refuses it when the required reading is above the limit.
func write(spec Spec, slots []Slot) (string, error) {
	listed, other, err := readFirstList(spec.Lane.ReadFirst)
	if err != nil {
		return "", err
	}

	spec.ReadFirst = listed
	emitted, err := Emit(spec)
	if err != nil {
		return "", err
	}

	text := Fill(emitted, slots)

	return text, SizeGate(len(text), other, ReadLimit)
}

// specOf returns the spec of the brief and the slots that fill it. It refuses a bad rules file, a bad model, a bad slots file and a bad cap.
func specOf(request Request) (Spec, []Slot, error) {
	rules, lean, err := leanRules(request)
	if err != nil {
		return Spec{}, nil, err
	}

	model, err := ParseModel(request.Name, request.Model)
	if err != nil {
		return Spec{}, nil, err
	}

	slots, err := slotsAndCap(request)
	if err != nil {
		return Spec{}, nil, err
	}

	return Spec{Kind: request.Kind, Model: model, Lane: request.Lane, Rules: lean, Taken: rules.Taken}, slots, nil
}

// leanRules loads the rules file of the project and cuts it for the kind of the brief.
func leanRules(request Request) (Rules, string, error) {
	rules, err := LoadRules(request.RulesPath)
	if err != nil {
		return Rules{}, "", err
	}

	lean, err := rules.Lean(request.Kind)

	return rules, lean, err
}

// slotsAndCap reads the slots file and refuses a context cap that is not above zero.
func slotsAndCap(request Request) ([]Slot, error) {
	slots, err := ReadSlots(request.SlotsFile)
	if err != nil {
		return nil, err
	}

	return slots, CheckCap(cmp.Or(request.Lane.Cap, ContextCap))
}

// readFirstList returns the paths of the read-first file and the bytes of the files they name.
func readFirstList(path string) ([]string, int, error) {
	if path == "" {
		return nil, 0, nil
	}

	if !isFile(path) {
		return nil, 0, refuse("the read-first list at %s is not a file. Write it with one path on each line.", path)
	}

	text, err := ReadText(path)
	if err != nil {
		return nil, 0, err
	}

	listed := awkLines(text)
	other, err := ReadBytes(listed)

	return listed, other, err
}

// record adds the row of the lane to the ledger and returns the lines to show.
// A lane that writes a row has a ledger kind. A review of a pull request writes a review row. Any other lane writes none.
func record(request Request, admission Admission, book Book) ([]string, error) {
	lane := request.Lane
	row := ledger.Request{Lane: request.Name, At: request.At, Body: lane.Body}

	switch {
	case admission.Kind != "":
		row.Key, row.Kind, row.Mechanism = lane.Key, admission.Kind, admission.Mechanism
	case lane.ReviewPR != "":
		row.Key, row.Kind = cmp.Or(lane.Key, lane.ReviewPR), ledger.KindReview
	default:
		return nil, nil
	}

	added, err := book.Add(row)
	if err != nil {
		return nil, err
	}

	return ledgerLines(row, added), nil
}

func ledgerLines(row ledger.Request, added ledger.Added) []string {
	var lines []string

	if added.FirstReview {
		closes := cmp.Or(strings.Join(added.Closes, " "), "none")
		lines = append(lines, "first review of "+row.Key+": recorded the issues it closes: "+closes)
	}

	return append(lines, "pr="+row.Key+" lane="+row.Lane+" kind="+string(row.Kind)+" fix lanes="+strconv.Itoa(added.FixLanes))
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
