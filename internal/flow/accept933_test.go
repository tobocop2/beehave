package flow

// Acceptance rows of #933: an author changes the workflow file under an open run and moves the run onto the new file.
// Each test prints one ROW line for each cell of the table of the ruling.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

const acc933Settle = 15 * time.Second

// acc933Adopt is `beehave resume <item> --on-this-file` of the library: the run of the item takes the workflow file as it is on disk now.
func acc933Adopt(c *Client, item string) error {
	return c.ResumeOnThisFile(item)
}

// acc933Answer answers the question of the item. It reports false when no question waits yet, so a poll calls it again.
func acc933Answer(c *Client) bool {
	return c.Reply(testItem, Reply{Text: "yes"}) == nil
}

// acc933Pin is the pin that the run replays against: the pin that adoption wrote, else the pin of the input.
func acc933Pin(r run) []workflow.PinnedFile {
	in, err := r.input()
	if err != nil {
		return nil
	}

	pin, err := pinOf(r.Status, in)
	if err != nil {
		return nil
	}

	return pin
}

// acc933Names is the flow of every case. now() is one recorded step, wait() records the question and the answer.
const (
	acc933Frontier = "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n"
	acc933Plain    = "def main(item):\n    now()\n"
	acc933Plain2   = "def main(item):\n    now()\n    now()\n"
)

// acc933Change is one change of the file under a run that waits at its question. After is true when the change sits after the step that the run reached.
type acc933Change struct {
	name  string
	text  string
	after bool
}

func acc933Changes() []acc933Change {
	return []acc933Change{
		{"changed question text (same steps)", "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What next?\")\n    now()\n", true},
		{"step added after the question", acc933Frontier + "    now()\n", true},
		{"step removed after the question", "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n", true},
		{"step added before the question", "def main(item):\n    now()\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n", false},
		{"step removed before the question", "def main(item):\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n", false},
		{"finished step replaced by another kind", "def main(item):\n    facts()\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n", false},
		{"open question renamed", "def main(item):\n    now()\n    wait(name = \"ask2\", question = \"What now?\")\n    now()\n", true},
	}
}

func acc933Row(t *testing.T, group, name string, ok bool, detail string) {
	t.Helper()

	verdict := "RED"
	if ok {
		verdict = "GREEN"
	}

	t.Logf("ROW %s | %s | %s | %s", group, name, verdict, strings.ReplaceAll(detail, "\n", " "))

	if !ok {
		t.Fail()
	}
}

func acc933Hash(t *testing.T, file string) string {
	t.Helper()

	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	for _, p := range pinned {
		if p.Path == "workflow.star" {
			return p.SHA256[:8]
		}
	}

	return ""
}

func acc933PinHash(r run) string {
	for _, p := range acc933Pin(r) {
		if p.Path == "workflow.star" {
			return p.SHA256[:8]
		}
	}

	return ""
}

func acc933Write(t *testing.T, file, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
}

// acc933Last reads the newest run of the test item until the read is true or the settle bound passes.
func acc933Last(t *testing.T, engine *Engine, until func(run) bool) run {
	t.Helper()

	var last run

	deadline := time.Now().Add(acc933Settle)
	for time.Now().Before(deadline) {
		got, found, err := latestRun(engine.store(), testItem)
		if err == nil && found {
			last = got
			if until(got) {
				return got
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	return last
}

func acc933Names(t *testing.T, engine *Engine, id string) string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, id)
	require.NoError(t, err)

	var names []string

	for _, s := range steps {
		if strings.Contains(s.StepName, "#") {
			names = append(names, s.StepName)
		}
	}

	return strings.Join(names, ",")
}

func acc933Err(r run) string {
	if r.Status.Error == nil {
		return ""
	}

	return r.Status.Error.Error()
}

// TestAccept933_ARecoveryOverAChangedFileTellsTheAuthorTheWay recovers an open run over each changed file, with no adoption. The run stays refused as at the base,
// and the refusal names the command that moves the run onto the file.
func TestAccept933_ARecoveryOverAChangedFileTellsTheAuthorTheWay(t *testing.T) {
	t.Parallel()

	for _, change := range acc933Changes() {
		h := newHarness(t)
		file := writeFlow(t, acc933Frontier)
		interrupted(t, h, file)
		acc933Write(t, file, change.text)

		engine := h.engine(t)
		_, err := runFileWithin(t, engine, file)
		text := ""
		if err != nil {
			text = err.Error()
		}

		refused := strings.Contains(text, "changed since the run started")
		names := strings.Contains(text, "--on-this-file")
		acc933Row(t, "recovery", change.name, refused && names, fmt.Sprintf("refused=%v names-the-command=%v err=%.90s", refused, names, text))
	}
}

// TestAccept933_ANewRunPinsTheNewFile ends a run, changes the file, and starts again: the new attempt pins the new bytes. This row holds at the base and must hold at the head.
func TestAccept933_ANewRunPinsTheNewFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, acc933Plain)
	engine := h.engine(t)

	_, err := runFileWithin(t, engine, file)
	require.NoError(t, err)

	acc933Write(t, file, acc933Plain2)

	_, err = runFileWithin(t, engine, file)
	last, found, readErr := latestRun(engine.store(), testItem)
	require.NoError(t, readErr)

	want := acc933Hash(t, file)
	acc933Row(t, "new run", "attempt 2 pins the file as it is now", err == nil && found && strings.HasSuffix(last.Status.ID, "r2") && acc933PinHash(last) == want,
		fmt.Sprintf("err=%v id=%s pin=%s want=%s", err, last.Status.ID, acc933PinHash(last), want))
}

// TestAccept933_AdoptMovesTheOpenRunOntoTheNewFile adopts each changed file for the open run.
// A change that no recorded step reaches continues on the new file. A change before the reached step ends the run at the first differing step, with DBOS naming it, and a restore plus a second adoption finishes the run.
func TestAccept933_AdoptMovesTheOpenRunOntoTheNewFile(t *testing.T) {
	t.Parallel()

	for _, change := range acc933Changes() {
		h := newHarness(t)
		file := writeFlow(t, acc933Frontier)
		original := acc933Hash(t, file)
		interrupted(t, h, file)
		expireLease(h)
		acc933Write(t, file, change.text)

		client := h.client(t, testVersion)
		adoptErr := acc933Adopt(client, testItem)

		if adoptErr != nil {
			acc933Row(t, "adopt", change.name, false, "adopt: "+adoptErr.Error())

			continue
		}

		if change.after {
			acc933Continues(t, h, client, file, change)

			continue
		}

		acc933FailsThenRestores(t, h, client, file, change, original)
	}
}

// acc933Continues checks a change after the reached step: the run goes on and ends done on the new pin.
func acc933Continues(t *testing.T, h *harness, client *Client, file string, change acc933Change) {
	t.Helper()

	engine := h.engine(t)
	changed := acc933Hash(t, file)
	last := acc933Last(t, engine, func(r run) bool { acc933Answer(client); return r.terminal() })
	names := acc933Names(t, engine, last.Status.ID)
	ok := stateOf(last.Status.Status) == runDone && acc933PinHash(last) == changed && strings.Contains(names, "now#1")
	acc933Row(t, "adopt", change.name, ok, fmt.Sprintf("state=%s pin=%s want=%s steps=%s err=%.60s", last.Status.Status, acc933PinHash(last), changed, names, acc933Err(last)))
}

// acc933FailsThenRestores checks a change before the reached step: the run fails at the first differing step, and a restore plus a second adoption finishes it.
func acc933FailsThenRestores(t *testing.T, h *harness, client *Client, file string, change acc933Change, original string) {
	t.Helper()

	engine := h.engine(t)
	last := acc933Last(t, engine, func(r run) bool { return r.terminal() })
	first := stateOf(last.Status.Status) == runFailed && strings.Contains(acc933Err(last), "was recorded when")

	acc933Write(t, file, acc933Frontier)
	restoreErr := acc933Adopt(client, testItem)
	again := acc933Last(t, engine, func(r run) bool { acc933Answer(client); return r.terminal() && stateOf(r.Status.Status) == runDone })
	ok := first && restoreErr == nil && stateOf(again.Status.Status) == runDone && acc933PinHash(again) == original
	acc933Row(t, "adopt", change.name, ok, fmt.Sprintf("first=%s err=%.70s restoreErr=%v then=%s pin=%s want=%s", last.Status.Status, acc933Err(last), restoreErr, again.Status.Status, acc933PinHash(again), original))
}
