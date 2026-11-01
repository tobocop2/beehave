package skypilot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/model"
)

const (
	fakeCluster = "obee-test"
	sshPrefix   = 3 // the words before the remote command: -o BatchMode=yes <cluster>
)

const fakeDF = "Filesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/sda1        104857600  10485760  94371840      10% /home\n"

// fakeJob is one job in the fake Slurm. polls counts the squeue calls, so the first answer is a live state and the later ones are the empty answer of a forgotten job.
type fakeJob struct {
	command  string
	canceled bool
	polls    int
	live     int   // the number of squeue calls that answer RUNNING; zero means one
	vanished bool  // the scheduler holds no record of the job
	logSizes []int // the size of the log at each stat call; the last size repeats
	stats    int
	blank    int // the number of sacct calls that answer nothing before the record appears
	lingers  int // the number of squeue calls after scancel that still answer RUNNING
}

// fakeHost is a sky command line and an ssh command line over one state. It prints the forms that the research file lists.
type fakeHost struct {
	mu       sync.Mutex
	broken   bool
	badDisk  bool
	up       bool
	draining bool
	jobs     map[string]*fakeJob
	next     int
	files    map[string]bool // the files in the login folder
	slots    []string        // the marker each slot job waits for, in the order the jobs started
	reuseID  bool            // sbatch gives the last job id again, as Slurm does after a restart
	drops    int             // the number of ssh calls that fail with the ssh exit code before the link comes back
	sbatches int             // the number of jobs submitted
	dropOn   string          // the first word of a command whose ssh call drops once
	noStop   bool            // scancel fails
	scancels int             // the number of scancel calls that took
	dropAll  string          // the first word of a command whose ssh call always drops
}

func newFakeHost(mode hosttest.Mode) *fakeHost {
	return &fakeHost{
		broken: mode == hosttest.Broken, badDisk: mode == hosttest.UnreadableDisk,
		jobs: map[string]*fakeJob{}, next: 41, files: map[string]bool{},
	}
}

// waitingSlots is the number of slot jobs whose marker is not in the login folder.
func (f *fakeHost) waitingSlots() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	waiting := 0

	for _, marker := range f.slots {
		if !f.files[marker] {
			waiting++
		}
	}

	return waiting
}

// slotMarker returns the marker that a slot command waits for.
func slotMarker(command string) string {
	_, rest, _ := strings.Cut(command, "test -e ")
	marker, _, _ := strings.Cut(rest, ";")

	return marker
}

func (f *fakeHost) sky() fakeRunner { return fakeRunner{run: f.runSky} }

func (f *fakeHost) ssh() fakeRunner { return fakeRunner{run: f.runSSH} }

type fakeRunner struct {
	run func(args []string) model.CommandResult
}

func (r fakeRunner) Run(_ context.Context, args []string) model.CommandResult { return r.run(args) }

func failure(text string) model.CommandResult { return model.CommandResult{ExitCode: 1, Stderr: text} }

func ok(out string) model.CommandResult { return model.CommandResult{Stdout: out} }

func (f *fakeHost) runSky(args []string) model.CommandResult {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.broken {
		return failure("sky: broken")
	}

	switch args[0] {
	case "launch":
		f.up = true

		return ok("")
	case "down":
		f.up, f.draining = false, false

		return ok("")
	case "exec":
		f.slots = append(f.slots, slotMarker(args[len(args)-1]))

		return ok("")
	case "status":
		if !f.up {
			return ok("[]\n")
		}

		return ok(fmt.Sprintf(`[{"name": %q, "status": "UP", "autostop": 60, "to_down": true}, {"name": "other", "status": "UP"}]`, fakeCluster))
	}

	return failure("sky: unknown command")
}

func (f *fakeHost) runSSH(args []string) model.CommandResult {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.broken || !f.up {
		return model.CommandResult{ExitCode: sshFailed, Stderr: "ssh: no route"}
	}

	line := strings.Join(args[sshPrefix:], " ")
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
		f.draining = words[3] == "State=DRAIN"

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

// dropped reports whether the ssh link drops for this call: a planned number of calls (a negative number is every call), or one call of a command.
func (f *fakeHost) dropped(command string) bool {
	switch {
	case f.drops < 0:
		return true
	case f.drops > 0:
		f.drops--

		return true
	case f.dropAll != "" && command == f.dropAll:
		return true
	case command == f.dropOn:
		f.dropOn = ""

		return true
	}

	return false
}

// other runs the commands that change no state of the host beyond a job.
func (f *fakeHost) other(words []string) model.CommandResult {
	switch words[0] {
	case "scancel":
		if f.noStop {
			return failure("scancel: denied")
		}

		f.scancels++
		f.jobs[words[1]].canceled = true

		return ok("")
	case "touch":
		f.files[words[1]] = true

		return ok("")
	case "stat":
		return f.stat(words[len(words)-1])
	case "pwd":
		return ok("/home/lane\n")
	}

	return failure("ssh: unknown command")
}

// stat prints the size of the log of a job, from the sizes the test planned. A job with none has no log.
func (f *fakeHost) stat(log string) model.CommandResult {
	job := f.jobs[strings.TrimSuffix(strings.TrimPrefix(log, "slurm-"), ".out")]
	if job == nil || len(job.logSizes) == 0 {
		return failure("stat: cannot stat " + log)
	}

	size := job.logSizes[min(job.stats, len(job.logSizes)-1)]
	job.stats++

	return ok(strconv.Itoa(size) + "\n")
}

func (f *fakeHost) df() model.CommandResult {
	if f.badDisk {
		return ok("df: cannot read the disk\n")
	}

	return ok(fakeDF)
}

func (f *fakeHost) sinfo(words []string) model.CommandResult {
	if words[len(words)-1] == "%N" {
		return ok("node1\nnode2\n")
	}

	if f.draining {
		return ok("drained\ndraining*\n")
	}

	return ok("idle\nmixed\n")
}

func (f *fakeHost) sbatch(rest string) model.CommandResult {
	command := rest[strings.Index(rest, "--wrap=")+len("--wrap="):]
	if !f.reuseID {
		f.next++
	}

	f.sbatches++

	id := strconv.Itoa(f.next)
	f.jobs[id] = &fakeJob{command: strings.Trim(command, "'")}

	return ok(id + "\n")
}

func (f *fakeHost) squeue(id string) model.CommandResult {
	job := f.jobs[id]
	job.polls++

	if job.command == hosttest.UnknownCommand {
		return ok("MYSTERY\n")
	}

	if job.vanished {
		return model.CommandResult{ExitCode: 1, Stderr: "slurm_load_jobs error: Invalid job id specified"}
	}

	if job.canceled && job.lingers > 0 {
		job.lingers--

		return ok("RUNNING\n")
	}

	if job.polls <= max(job.live, 1) && !job.canceled {
		return ok("RUNNING\n")
	}

	return model.CommandResult{ExitCode: 1, Stderr: "slurm_load_jobs error: Invalid job id specified"}
}

func (f *fakeHost) sacct(id string) model.CommandResult {
	job := f.jobs[id]

	switch {
	case job.vanished:
		return ok("")
	case job.blank > 0:
		job.blank--

		return ok("")
	case job.canceled:
		return ok("CANCELLED by 1000\n") //nolint:misspell // Slurm's spelling
	case job.command == hosttest.FailingCommand:
		return ok("FAILED\n")
	}

	return ok("COMPLETED\n")
}

func newTestHost(t *testing.T, mode hosttest.Mode) host.Host {
	t.Helper()

	f := newFakeHost(mode)
	config := model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour}

	h, err := New(fakeCluster, config, f.sky(), f.ssh())
	if err != nil {
		t.Fatal(err)
	}

	h.poll = time.Millisecond
	h.bank = func(context.Context) error { return nil }

	return h
}
