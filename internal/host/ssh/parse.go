package ssh

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/model"
)

var (
	errBadJobID    = errors.New("a job id is the unit name that Submit returned: beehave, a dash and letters or digits")
	errUnitText    = errors.New("systemd gave a unit record that beehave does not read")
	errUnitState   = errors.New("systemd gave a unit state that beehave does not know")
	errLingerText  = errors.New("loginctl gave an answer that beehave does not read")
	errVersionText = errors.New("systemd-run gave a version line that beehave does not read")
)

// isRecorded says whether a state is one of the final states that a poll writes beside the log. A cancel has its own marker, and died has no record.
func isRecorded(state model.JobState) bool {
	return state == model.JobSucceeded || state == model.JobFailed || state == model.JobTimedOut
}

// isFault says whether a Result value is one that systemd documents for a service that ended with a fault (systemd.service, "Result").
// A job that ended with any of them failed; timeout is the run limit and has its own state.
func isFault(result string) bool {
	switch result {
	case "exit-code", "signal", "core-dump", "oom-kill", "watchdog", "resources", "start-limit-hit", "protocol", "exec-condition":
		return true
	}

	return false
}

var unitPattern = regexp.MustCompile(`^beehave-[A-Za-z0-9]+$`)

// The words of the read of one poll. The script prints the cancel marker, the properties of the unit and the size of its log (-1 for none).
// systemctl show prints the keys in its own order, so the parser reads them by key.
//
// A unit is stopped and reset after its work is banked, and then systemd has no record of it. The poll that first reads a final state writes
// that state as an empty file beside the log (`<base>.succeeded`, `.failed` or `.timed_out`), and every later poll reads the file back.
// This keeps the result of a job readable after its release, as the accounting of Slurm does.
const (
	canceledLine = "beehave-canceled"
	resultPrefix = "beehave-result "
	sizePrefix   = "beehave-size "
	observeShell = `if test -e "$2.canceled"; then echo ` + canceledLine + `; fi
for s in succeeded failed timed_out; do if test -e "$2.$s"; then echo "` + resultPrefix + `$s"; fi; done
systemctl --user show -p LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus "$1" || exit 3
echo "` + sizePrefix + `$(stat -c %s "$2.log" 2>/dev/null || echo -1)"`
)

// minSystemd is the oldest systemd whose transient units write a log with append:.
const minSystemd = 240

// parseJobID reads a job id that Submit returned. It refuses any other text, so none reaches the command line of the host.
func parseJobID(id model.JobID) (jobwait.Job, error) {
	if !unitPattern.MatchString(string(id)) {
		return jobwait.Job{}, fmt.Errorf("%w: got %q", errBadJobID, string(id))
	}

	return jobwait.Job{ID: string(id)}, nil
}

// reading is what one poll read about the unit: the marker of a cancel and the properties. The size of the log comes back beside it.
type reading struct {
	canceled                  bool
	recorded                  model.JobState // the final state that an earlier poll wrote beside the log; empty for none
	load, active, sub, result string
	code, status              int
}

// parseReading reads the output of the poll script. A key that is missing, or a number that is not one, is an error: beehave never guesses a state.
func parseReading(out string) (reading, int64, error) {
	var r reading

	size, seen := int64(-1), map[string]string{}

	for _, line := range model.NonEmptyLines(out) {
		var err error
		if size, err = r.take(line, size, seen); err != nil {
			return reading{}, -1, err
		}
	}

	r, err := fill(r, seen)

	return r, size, err
}

// take reads one line of the output: the marker, the size of the log (which it returns) or a key and its value.
func (r *reading) take(line string, size int64, seen map[string]string) (int64, error) {
	key, value, isPair := strings.Cut(line, "=")

	switch {
	case line == canceledLine:
		r.canceled = true
	case strings.HasPrefix(line, resultPrefix):
		return size, r.takeResult(line)
	case strings.HasPrefix(line, sizePrefix):
		n, err := strconv.ParseInt(strings.TrimPrefix(line, sizePrefix), 10, 64)
		if err != nil {
			return size, fmt.Errorf("%w: %q", errUnitText, line)
		}

		return n, nil
	case isPair:
		seen[key] = value
	}

	return size, nil
}

// takeResult reads the line of a state that an earlier poll recorded. Any other state is an error.
func (r *reading) takeResult(line string) error {
	state := model.JobState(strings.TrimPrefix(line, resultPrefix))
	if !isRecorded(state) {
		return fmt.Errorf("%w: %q", errUnitText, line)
	}

	r.recorded = state

	return nil
}

func fill(r reading, seen map[string]string) (reading, error) {
	for _, key := range []string{"LoadState", "ActiveState", "SubState", "Result", "ExecMainCode", "ExecMainStatus"} {
		if _, found := seen[key]; !found {
			return reading{}, fmt.Errorf("%w: no %s", errUnitText, key)
		}
	}

	var codeErr, statusErr error

	r.load, r.active, r.sub, r.result = seen["LoadState"], seen["ActiveState"], seen["SubState"], seen["Result"]
	r.code, codeErr = strconv.Atoi(seen["ExecMainCode"])
	r.status, statusErr = strconv.Atoi(seen["ExecMainStatus"])

	if err := errors.Join(codeErr, statusErr); err != nil {
		return reading{}, fmt.Errorf("%w: %w", errUnitText, err)
	}

	return r, nil
}

// state maps the record of a unit to a job state, by the values that the probe of systemd 255 measured.
// The marker of a cancel decides when the unit is gone or has ended: a stopped unit reads not-found, success or timeout.
// A unit that still runs after the stop is not final, so the bank waits for the end of the processes.
func (r reading) state() (model.JobState, error) {
	switch {
	case r.load == "not-found":
		return r.gone(), nil
	case r.load != "loaded":
		return model.JobUnknown, r.unknown()
	case r.active == "activating":
		return model.JobPending, nil
	case r.active == "active" && r.sub == "running", isChanging(r.active):
		return model.JobRunning, nil
	case r.active == "active" && r.sub == "exited":
		return r.exited(), nil
	case r.active == "failed":
		return r.failed()
	}

	return model.JobUnknown, r.unknown()
}

// isChanging says whether an ActiveState is one of a unit that systemd is stopping or reloading. The unit is not final, so it still runs,
// as Slurm reads COMPLETING. A job that ignores SIGTERM stays in deactivating until TimeoutStopSec ends.
func isChanging(active string) bool {
	return active == "deactivating" || active == "reloading" || active == "refreshing"
}

// gone is a unit that systemd has no record of. A cancel marker means canceled. Without it, the final state that an earlier poll wrote
// is the result. A unit with neither never ran or was lost: it is died.
func (r reading) gone() model.JobState {
	switch {
	case r.canceled:
		return model.JobCanceled
	case r.recorded != "":
		return r.recorded
	}

	return model.JobDied
}

// exited is a unit that ended with its main process. Only exit status 0 from a normal exit is success: SIGTERM on the main process reads active, exited and success.
func (r reading) exited() model.JobState {
	switch {
	case r.canceled:
		return model.JobCanceled
	case r.code == 1 && r.status == 0:
		return model.JobSucceeded
	}

	return model.JobFailed
}

func (r reading) failed() (model.JobState, error) {
	switch {
	case r.canceled:
		return model.JobCanceled, nil
	case r.result == "timeout":
		return model.JobTimedOut, nil
	case isFault(r.result):
		return model.JobFailed, nil
	}

	return model.JobUnknown, r.unknown()
}

func (r reading) unknown() error {
	return fmt.Errorf("%w: LoadState=%s ActiveState=%s SubState=%s Result=%s", errUnitState, r.load, r.active, r.sub, r.result)
}

// parseLinger reads `loginctl show-user -p Linger`.
func parseLinger(out string) (bool, error) {
	switch strings.TrimSpace(out) {
	case "Linger=yes":
		return true, nil
	case "Linger=no":
		return false, nil
	}

	return false, fmt.Errorf("%w: %q", errLingerText, model.FirstLine(out))
}

// parseSystemd reads the first line of `systemd-run --version`: the word systemd and the version number.
func parseSystemd(out string) (int, error) {
	words := strings.Fields(model.FirstLine(out))
	if len(words) < 2 || words[0] != "systemd" {
		return 0, fmt.Errorf("%w: %w: %q", ErrSystemd, errVersionText, model.FirstLine(out))
	}

	version, err := strconv.Atoi(words[1])
	if err != nil {
		return 0, fmt.Errorf("%w: %w: %q", ErrSystemd, errVersionText, model.FirstLine(out))
	}

	return version, nil
}
