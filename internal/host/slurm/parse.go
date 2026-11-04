package slurm

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/model"
)

// The refusals of the parsers. Each wraps the text that was not recognized.
var (
	errJobID    = errors.New("sbatch gave an answer that is not a job id")
	errBadJobID = errors.New("a job id is the number and the nonce that Submit returned, joined by a dash")
	errJobText  = errors.New("the scheduler gave a job state that beehave does not know")
	errNodeText = errors.New("sinfo gave a node state that beehave does not know")
	errNodeDown = errors.New("a node is down, so the host cannot take a job")
	errNodeName = errors.New("sinfo gave a node name that beehave does not accept")
	errNoNodes  = errors.New("sinfo listed no node")
)

var (
	jobIDPattern  = regexp.MustCompile(`^([0-9]+)(;[A-Za-z0-9_.-]+)?$`)
	handlePattern = regexp.MustCompile(`^([0-9]+)-([A-Za-z0-9]+)$`)
	nodePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const nodeSuffixes = "*~#!%$@^-"

// parseSubmitted reads the answer of `sbatch --parsable`: a job id, then an optional semicolon and a cluster name.
func parseSubmitted(out string) (string, error) {
	match := jobIDPattern.FindStringSubmatch(strings.TrimSpace(out))
	if match == nil {
		return "", fmt.Errorf("%w: %q", errJobID, model.FirstLine(out))
	}

	return match[1], nil
}

// parseJobID reads a job id that Submit returned. It refuses any other text, so none reaches the scheduler command line.
func parseJobID(id model.JobID) (jobwait.Job, error) {
	match := handlePattern.FindStringSubmatch(string(id))
	if match == nil {
		return jobwait.Job{}, fmt.Errorf("%w: got %q", errBadJobID, string(id))
	}

	return jobwait.Job{ID: match[1], Nonce: match[2]}, nil
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
	lines := model.NonEmptyLines(out)
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
	lines := model.NonEmptyLines(out)
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

// nodeDrains reports whether a node state means the node takes no new job, and whether beehave knows the state.
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
	lines := model.NonEmptyLines(out)
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

// nodeLineDrains reads one node state. A node that is down and a state beehave does not know are refused.
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
