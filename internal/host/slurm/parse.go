package slurm

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// The refusals of the parsers. Each wraps the text that was not recognized.
var (
	errJobID    = errors.New("sbatch gave an answer that is not a job id")
	errBadJobID = errors.New("a job id is the number and the nonce that Submit returned, joined by a dash")
	errJobText  = errors.New("the scheduler gave a job state that obee does not know")
	errNodeText = errors.New("sinfo gave a node state that obee does not know")
	errNodeDown = errors.New("a node is down, so the host cannot take a job")
	errNodeName = errors.New("sinfo gave a node name that obee does not accept")
	errNoNodes  = errors.New("sinfo listed no node")
	errDiskText = errors.New("df gave an answer that obee does not read")
)

var (
	jobIDPattern  = regexp.MustCompile(`^([0-9]+)(;[A-Za-z0-9_.-]+)?$`)
	handlePattern = regexp.MustCompile(`^([0-9]+)-([A-Za-z0-9]+)$`)
	nodePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const (
	nodeSuffixes    = "*~#!%$@^-"
	bytesPerKiB     = 1024
	diskHeaderWords = "Filesystem 1024-blocks Used Available Capacity Mounted on"
	diskDataWords   = 6
	diskAvailable   = 3
)

// parseSubmitted reads the answer of `sbatch --parsable`: a job id, then an optional semicolon and a cluster name.
func parseSubmitted(out string) (string, error) {
	match := jobIDPattern.FindStringSubmatch(strings.TrimSpace(out))
	if match == nil {
		return "", fmt.Errorf("%w: %q", errJobID, model.FirstLine(out))
	}

	return match[1], nil
}

// Job is what the job id of this adapter holds: the Slurm job id and the nonce that Submit made for the slot of the job.
type Job struct {
	ID    string
	Nonce string
}

// String is the job id that Submit returns: the Slurm job id, a dash, and the nonce.
func (j Job) String() string { return j.ID + "-" + j.Nonce }

// parseJobID reads a job id that Submit returned. It refuses any other text, so none reaches the scheduler command line.
func parseJobID(id model.JobID) (Job, error) {
	match := handlePattern.FindStringSubmatch(string(id))
	if match == nil {
		return Job{}, fmt.Errorf("%w: got %q", errBadJobID, string(id))
	}

	return Job{ID: match[1], Nonce: match[2]}, nil
}

// slurmJobState maps a Slurm state name to a job state. The names are Slurm's own spelling.
func slurmJobState(name string) (model.JobState, bool) {
	switch name {
	case "PENDING", "CONFIGURING", "REQUEUED", "REQUEUE_HOLD", "REQUEUE_FED", "RESV_DEL_HOLD", "SPECIAL_EXIT":
		return model.JobPending, true
	case "RUNNING", "COMPLETING", "SUSPENDED", "STOPPED", "SIGNALING", "STAGE_OUT", "RESIZING":
		return model.JobRunning, true
	case "COMPLETED":
		return model.JobSucceeded, true
	case "TIMEOUT":
		return model.JobTimedOut, true
	case "FAILED", "NODE_FAIL", "OUT_OF_MEMORY", "BOOT_FAIL", "DEADLINE", "PREEMPTED", "REVOKED":
		return model.JobFailed, true
	case "CANCELLED": //nolint:misspell // Slurm's spelling
		return model.JobCanceled, true
	}

	return model.JobUnknown, false
}

// parseJobState reads one line of `squeue -h -o %T` or `sacct -X -n -P -o State`. Slurm may add text after the state, such as a user id or a plus sign.
func parseJobState(out string) (model.JobState, error) {
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		return model.JobUnknown, fmt.Errorf("%w: %q", errJobText, model.FirstLine(out))
	}

	word, _, _ := strings.Cut(lines[0], " ")
	word, exit, _ := strings.Cut(word, "|")

	state, found := slurmJobState(strings.TrimSuffix(word, "+"))
	if !found {
		return model.JobUnknown, fmt.Errorf("%w: %q", errJobText, lines[0])
	}

	// `timeout` ends a command with exit status 124. Slurm calls that job FAILED, so the exit code tells the run limit from a red run.
	if state == model.JobFailed && strings.HasPrefix(exit, timeoutExit+":") {
		return model.JobTimedOut, nil
	}

	return state, nil
}

// parseNodes reads `sinfo -h -N -o %N`. A node name that could change a command line is refused.
func parseNodes(out string) ([]string, error) {
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return nil, errNoNodes
	}

	var nodes []string

	for _, line := range lines {
		if !nodePattern.MatchString(line) {
			return nil, fmt.Errorf("%w: %q", errNodeName, line)
		}

		if !slices.Contains(nodes, line) {
			nodes = append(nodes, line)
		}
	}

	return nodes, nil
}

// nodeDrains reports whether a node state means the node takes no new job, and whether obee knows the state.
func nodeDrains(name string) (drains, known bool) {
	switch name {
	case "drained", "draining":
		return true, true
	case "allocated", "idle", "mixed", "completing", "planned", "reserved":
		return false, true
	}

	return false, false
}

// parseDraining reads `sinfo -h -N -o %T` and reports whether any node drains or is drained. A state it does not know is refused.
func parseDraining(out string) (bool, error) {
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return false, errNoNodes
	}

	draining := false

	for _, line := range lines {
		state, err := nodeLineDrains(line)
		if err != nil {
			return false, err
		}

		draining = draining || state
	}

	return draining, nil
}

// nodeLineDrains reads one node state. A node that is down and a state obee does not know are refused.
func nodeLineDrains(line string) (bool, error) {
	name := strings.ToLower(strings.TrimRight(line, nodeSuffixes))
	if name == "down" {
		return false, fmt.Errorf("%w: %q. Bring the node back with `scontrol update NodeName=<node> State=RESUME` on the head node", errNodeDown, line)
	}

	drains, found := nodeDrains(name)
	if !found {
		return false, fmt.Errorf("%w: %q", errNodeText, line)
	}

	return drains, nil
}

// parseFreeBytes reads `df -P -k` for one file system and returns the free space in bytes.
func parseFreeBytes(out string) (uint64, error) {
	lines := nonEmptyLines(out)
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != diskHeaderWords {
		return 0, fmt.Errorf("%w: %q", errDiskText, model.FirstLine(out))
	}

	words := strings.Fields(lines[1])
	if len(words) != diskDataWords {
		return 0, fmt.Errorf("%w: %q", errDiskText, lines[1])
	}

	kib, err := strconv.ParseUint(words[diskAvailable], 10, 64)
	if err != nil || kib > math.MaxUint64/bytesPerKiB {
		return 0, fmt.Errorf("%w: %q", errDiskText, lines[1])
	}

	return kib * bytesPerKiB, nil
}

func nonEmptyLines(text string) []string {
	var lines []string

	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
