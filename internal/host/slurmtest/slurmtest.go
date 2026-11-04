// Package slurmtest is a fake Slurm cluster behind an ssh command line, for the tests of every adapter that queues jobs through the slurm package.
// It prints the forms that the research file lists.
package slurmtest

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	// Target is the name of the head node in the ssh config.
	Target    = "obee-test"
	sshFailed = 255 // the exit status of ssh for a link that dropped
	sshPrefix = 3   // the words before the remote command: -o BatchMode=yes <cluster>
)

// DF is the answer of `df -P -k .` on a node with free space.
const DF = hosttest.DF

// Job is one job in the fake Slurm. Polls counts the squeue calls, so the first answer is a live state and the later ones are the empty answer of a forgotten job.
type Job struct {
	Command  string
	Canceled bool
	Polls    int
	Live     int   // the number of squeue calls that answer RUNNING; zero means one
	Vanished bool  // the scheduler holds no record of the job
	LogSizes []int // the size of the log at each stat call; the last size repeats
	Stats    int
	Blank    int // the number of sacct calls that answer nothing before the record appears
	Lingers  int // the number of squeue calls after scancel that still answer RUNNING
}

// Cluster is the state of a fake Slurm cluster. A test sets the fields to plan a failure. Mu guards them while a runner answers.
type Cluster struct {
	Mu       sync.Mutex
	Broken   bool
	BadDisk  bool
	Up       bool
	Draining bool
	Jobs     map[string]*Job
	Next     int
	Files    map[string]bool // the files in the login folder
	Slots    []string        // the marker each slot job waits for, in the order the jobs started
	ReuseID  bool            // sbatch gives the last job id again, as Slurm does after a restart
	Drops    int             // the number of ssh calls that fail with the ssh exit code before the link comes back
	Sbatches int             // the number of jobs submitted
	DropOn   string          // the first word of a command whose ssh call drops once
	NoStop   bool            // scancel fails
	Scancels int             // the number of scancel calls that took
	DropAll  string          // the first word of a command whose ssh call always drops
}

// New returns a cluster that is down. A test sets Up before it calls the ssh runner.
func New(mode hosttest.Mode) *Cluster {
	return &Cluster{
		Broken: mode == hosttest.Broken, BadDisk: mode == hosttest.UnreadableDisk,
		Jobs: map[string]*Job{}, Next: 41, Files: map[string]bool{},
	}
}

// WaitingSlots is the number of slot jobs whose marker is not in the login folder.
func (f *Cluster) WaitingSlots() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	waiting := 0

	for _, marker := range f.Slots {
		if !f.Files[marker] {
			waiting++
		}
	}

	return waiting
}

// slotMarker returns the marker that a slot command waits for.

func (f *Cluster) SSH() proc.Runner {
	return fakeRunner{run: f.runSSH}
}

type fakeRunner struct {
	run func(args []string) model.CommandResult
}

func (r fakeRunner) Run(_ context.Context, args []string) model.CommandResult { return r.run(args) }

func failure(text string) model.CommandResult { return model.CommandResult{ExitCode: 1, Stderr: text} }

func ok(out string) model.CommandResult { return model.CommandResult{Stdout: out} }

func (f *Cluster) runSSH(args []string) model.CommandResult {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	if !f.Up {
		return model.CommandResult{ExitCode: sshFailed, Stderr: "ssh: no route"}
	}

	if f.Broken {
		return failure("remote command failed")
	}

	line := strings.Join(args[commandStart(args):], " ")
	words := strings.Fields(line)

	if f.dropped(words[0]) {
		return model.CommandResult{ExitCode: sshFailed, Stderr: "ssh: connection reset"}
	}

	switch words[0] {
	case "df":
		return f.df()
	case "sinfo":
		return f.sinfo(words)
	case "scontrol":
		f.Draining = words[3] == "State=DRAIN"

		return ok("")
	case "sbatch":
		return f.sbatch(line)
	case "squeue":
		return f.squeue(words[3])
	case "sacct":
		return f.sacct(words[5])
	}

	return f.other(words)
}

// commandStart is the index of the first word of the remote command. A link to a machine puts its options, then "--", then the target before it;
// a link whose ssh config entry holds the options sends only -o BatchMode=yes and the target.
func commandStart(args []string) int {
	for i, arg := range args {
		if arg == "--" {
			return i + 2
		}
	}

	return sshPrefix
}

// dropped reports whether the ssh link drops for this call: a planned number of calls (a negative number is every call), or one call of a command.
func (f *Cluster) dropped(command string) bool {
	switch {
	case f.Drops < 0:
		return true
	case f.Drops > 0:
		f.Drops--

		return true
	case f.DropAll != "" && command == f.DropAll:
		return true
	case command == f.DropOn:
		f.DropOn = ""

		return true
	}

	return false
}

// other runs the commands that change no state of the host beyond a job.
func (f *Cluster) other(words []string) model.CommandResult {
	switch words[0] {
	case "scancel":
		if f.NoStop {
			return failure("scancel: denied")
		}

		job := f.Jobs[words[1]]
		if job == nil {
			return failure("scancel: invalid job id " + words[1])
		}

		f.Scancels++
		job.Canceled = true

		return ok("")
	case "touch":
		f.Files[words[1]] = true

		return ok("")
	case "stat":
		return f.stat(words[len(words)-1])
	case "pwd":
		return ok("/home/lane\n")
	}

	return failure("ssh: unknown command")
}

// stat prints the size of the log of a job, from the sizes the test planned. A job with none has no log.
func (f *Cluster) stat(log string) model.CommandResult {
	job := f.Jobs[strings.TrimSuffix(strings.TrimPrefix(log, "slurm-"), ".out")]
	if job == nil || len(job.LogSizes) == 0 {
		return failure("stat: cannot stat " + log)
	}

	size := job.LogSizes[min(job.Stats, len(job.LogSizes)-1)]
	job.Stats++

	return ok(strconv.Itoa(size) + "\n")
}

func (f *Cluster) df() model.CommandResult {
	if f.BadDisk {
		return ok("df: cannot read the disk\n")
	}

	return ok(DF)
}

func (f *Cluster) sinfo(words []string) model.CommandResult {
	if words[len(words)-1] == "%N" {
		return ok("node1\nnode2\n")
	}

	if f.Draining {
		return ok("drained\ndraining*\n")
	}

	return ok("idle\nmixed\n")
}

func (f *Cluster) sbatch(rest string) model.CommandResult {
	command := rest[strings.Index(rest, "--wrap=")+len("--wrap="):]
	if !f.ReuseID {
		f.Next++
	}

	f.Sbatches++

	id := strconv.Itoa(f.Next)
	f.Jobs[id] = &Job{Command: strings.ReplaceAll(strings.TrimSuffix(command, "'"), `'\''`, "'")}

	return ok(id + "\n")
}

func (f *Cluster) squeue(id string) model.CommandResult {
	job := f.Jobs[id]
	job.Polls++

	if job.Command == hosttest.UnknownCommand {
		return ok("MYSTERY\n")
	}

	if job.Vanished {
		return model.CommandResult{ExitCode: 1, Stderr: "slurm_load_jobs error: Invalid job id specified"}
	}

	if job.Canceled && job.Lingers > 0 {
		job.Lingers--

		return ok("RUNNING\n")
	}

	if job.Polls <= max(job.Live, 1) && !job.Canceled {
		return ok("RUNNING\n")
	}

	return model.CommandResult{ExitCode: 1, Stderr: "slurm_load_jobs error: Invalid job id specified"}
}

func (f *Cluster) sacct(id string) model.CommandResult {
	job := f.Jobs[id]

	switch {
	case job.Vanished:
		return ok("")
	case job.Blank > 0:
		job.Blank--

		return ok("")
	case job.Canceled:
		return ok("CANCELLED by 1000\n") //nolint:misspell // Slurm's spelling
	case job.Command == hosttest.FailingCommand, hosttest.Mangled(job.Command):
		return ok("FAILED\n")
	}

	return ok("COMPLETED\n")
}
