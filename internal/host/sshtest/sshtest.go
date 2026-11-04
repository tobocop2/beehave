// Package sshtest is a fake Linux machine with systemd behind an ssh command line, for the tests of the ssh adapter.
// It prints the forms that the probe of systemd 255 recorded: the property values of a transient unit for each way a job can end.
package sshtest

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	sshFailed = 255 // the exit status of ssh for a link that dropped
	notLoaded = 5   // the exit status of systemctl stop for a unit that is not loaded
	// User is the login user of the machine.
	User = "lane"
	// Root is the login folder of the machine.
	Root = "/home/" + User
)

// Unit is one transient unit on the fake machine. Polls counts the reads of its properties.
type Unit struct {
	Command  string   // the command of the job, decoded from the base64 that the adapter sent
	Args     []string // the systemd-run words
	Live     int      // the number of reads that answer a running unit; zero means one
	Vanished bool     // the user manager holds no record of the unit
	Stopped  bool
	Lingers  int // the number of reads after a stop that still answer a running unit
	Polls    int
	Stats    int
	LogSizes []int    // the size of the log at each read; the last size repeats
	Raw      []string // the properties each read prints in place of the planned ones; the last text repeats
	Reset    bool
}

// Machine is the state of a fake machine. A test sets the fields to plan a failure. Mu guards them while a runner answers.
type Machine struct {
	Mu          sync.Mutex
	Broken      bool // every command fails with exit 1
	BadDisk     bool
	Offline     bool // the ssh link does not connect
	LingerOff   bool
	NoSystemd   bool   // the user manager does not run
	SystemdText string // the first line of systemd-run --version; empty means systemd 255
	Units       map[string]*Unit
	Files       map[string]bool // markers and folders, by path
	Calls       [][]string      // the remote words of every call, unquoted
	Next        int
	Drops       int    // the number of calls that fail with the ssh exit code before the link comes back
	DropOn      string // the first word of a command whose call drops once
	DropAll     string // the first word of a command whose call always drops
	NoStop      bool   // systemctl stop fails
	NoRun       bool   // systemd-run fails
	Options     [][]string
	Fail        map[string]int    // the exit status of every command whose words start with the key
	Say         map[string]string // the stdout of every command whose words start with the key
}

// New returns a machine. A test builds the link over SSH().
func New(mode hosttest.Mode) *Machine {
	return &Machine{
		Broken: mode == hosttest.Broken, BadDisk: mode == hosttest.UnreadableDisk, Offline: mode == hosttest.Unreachable,
		Units: map[string]*Unit{}, Files: map[string]bool{},
	}
}

// SSH returns the ssh command line of the machine.
func (m *Machine) SSH() proc.Runner { return runner{m} }

type runner struct{ m *Machine }

func (r runner) Run(_ context.Context, args []string) model.CommandResult { return r.m.run(args) }

func failure(code int, text string) model.CommandResult {
	return model.CommandResult{ExitCode: code, Stderr: text}
}

func ok(out string) model.CommandResult { return model.CommandResult{Stdout: out} }

func (m *Machine) run(args []string) model.CommandResult {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	words, options := split(args)
	m.Calls = append(m.Calls, words)
	m.Options = append(m.Options, options)

	switch {
	case m.Offline:
		return failure(sshFailed, "ssh: no route")
	case m.Broken:
		return failure(1, "ssh: broken")
	case len(words) == 0:
		return failure(1, "ssh: no command")
	case m.dropped(words[0]):
		return failure(sshFailed, "ssh: connection reset")
	}

	line := strings.Join(words, " ")

	for prefix, code := range m.Fail {
		if strings.HasPrefix(line, prefix) {
			return failure(code, "planned failure of "+prefix)
		}
	}

	for prefix, text := range m.Say {
		if strings.HasPrefix(line, prefix) {
			return ok(text)
		}
	}

	return m.command(words)
}

// split returns the unquoted words after the target, and the words before the "--".
func split(args []string) (words, options []string) {
	at := 0

	for i, arg := range args {
		if arg == "--" {
			at = i

			break
		}
	}

	for _, arg := range args[at+2:] {
		words = append(words, unquote(arg))
	}

	return words, args[:at]
}

func unquote(word string) string {
	if len(word) >= 2 && word[0] == '\'' && word[len(word)-1] == '\'' {
		return strings.ReplaceAll(word[1:len(word)-1], `'\''`, "'")
	}

	return word
}

func (m *Machine) dropped(command string) bool {
	switch {
	case m.Drops < 0:
		return true
	case m.Drops > 0:
		m.Drops--

		return true
	case m.DropAll != "" && command == m.DropAll:
		return true
	case command == m.DropOn:
		m.DropOn = ""

		return true
	}

	return false
}

func (m *Machine) command(words []string) model.CommandResult {
	switch words[0] {
	case "id":
		return ok(User + "\n")
	case "pwd":
		return ok(Root + "\n")
	case "loginctl":
		return m.linger()
	case "systemctl":
		return m.systemctl(words)
	case "systemd-run":
		return m.systemdRun(words)
	case "sh":
		return m.observe(words)
	case "df":
		return m.df()
	case "tail":
		return ok("log text\n")
	}

	return m.files(words)
}

// files runs the commands that change or test a file.
func (m *Machine) files(words []string) model.CommandResult {
	switch words[0] {
	case "mkdir":
		m.Files[words[len(words)-1]+"/"] = true

		return ok("")
	case "touch":
		m.Files[words[1]] = true

		return ok("")
	case "rm":
		delete(m.Files, words[len(words)-1])

		return ok("")
	case "test":
		if words[1] == "-d" {
			return ok("")
		}

		if m.Files[words[2]] || m.Files[words[2]+"/"] {
			return ok("")
		}

		return failure(1, "")
	}

	return failure(127, "sh: "+words[0]+": not found")
}

func (m *Machine) linger() model.CommandResult {
	if m.LingerOff {
		return ok("Linger=no\n")
	}

	return ok("Linger=yes\n")
}

func (m *Machine) df() model.CommandResult {
	if m.BadDisk {
		return ok("df: cannot read the disk\n")
	}

	return ok(hosttest.DF)
}

func (m *Machine) systemctl(words []string) model.CommandResult {
	switch words[2] {
	case "is-system-running":
		if m.NoSystemd {
			return failure(1, "Failed to connect to bus: No medium found")
		}

		return ok("running\n")
	case "stop":
		return m.stop(words[3])
	case "reset-failed":
		if u := m.Units[words[3]]; u != nil {
			u.Reset = true
		}

		return ok("")
	}

	return failure(1, "systemctl: unknown verb")
}

func (m *Machine) stop(name string) model.CommandResult {
	u := m.Units[name]

	switch {
	case m.NoStop:
		return failure(1, "Failed to stop "+name)
	case u == nil || u.Vanished || u.Reset:
		return failure(notLoaded, "Unit "+name+" not loaded.")
	}

	u.Stopped = true

	return ok("")
}

func (m *Machine) systemdRun(words []string) model.CommandResult {
	if words[1] == "--version" {
		if m.SystemdText != "" {
			return ok(m.SystemdText + "\n")
		}

		return ok("systemd 255 (255.4-1ubuntu8.18)\n+PAM +AUDIT\n")
	}

	if m.NoRun {
		return failure(1, "Failed to start transient service unit")
	}

	name := ""

	for _, word := range words {
		if v, found := strings.CutPrefix(word, "--unit="); found {
			name = v
		}
	}

	m.Units[name] = &Unit{Command: decode(words[len(words)-1]), Args: words}
	m.Next++

	return ok("")
}

// Undecoded starts the command of a unit whose eval line did not carry the command as base64. Such a command is never equal to the text
// that was sent, so a transport that passes the text as plain shell words fails the conformance suite.
const Undecoded = "undecoded: "

// decode returns the command that the adapter sent in the eval line. Only the one shape of the adapter is read: eval "$(printf %s <base64> | base64 -d)".
func decode(line string) string {
	rest, found := strings.CutPrefix(line, `eval "$(printf %s `)
	if !found {
		return Undecoded + line
	}

	encoded, tail, _ := strings.Cut(rest, " |")
	if tail != ` base64 -d)"` {
		return Undecoded + line
	}

	text, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Undecoded + line
	}

	return string(text)
}

// observe answers the one read of a poll: the cancel marker, the properties of the unit and the size of its log.
// The words are sh -c <script> sh <unit> <base>.
func (m *Machine) observe(words []string) model.CommandResult {
	unit, base := words[len(words)-2], words[len(words)-1]
	u := m.Units[unit]

	var lines []string
	if m.Files[base+".canceled"] {
		lines = append(lines, "beehave-canceled")
	}

	for _, state := range []string{"succeeded", "failed", "timed_out"} {
		if m.Files[base+"."+state] {
			lines = append(lines, "beehave-result "+state)
		}
	}

	lines = append(lines, strings.TrimSuffix(m.properties(u), "\n"), "beehave-size "+strconv.Itoa(m.size(u)))

	return ok(strings.Join(lines, "\n") + "\n")
}

func (m *Machine) size(u *Unit) int {
	if u == nil || len(u.LogSizes) == 0 {
		return -1
	}

	size := u.LogSizes[min(u.Stats, len(u.LogSizes)-1)]
	u.Stats++

	return size
}

const notFound = "LoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainCode=0\nExecMainStatus=0\n"

// properties prints what `systemctl --user show` prints for the unit. A unit that is gone prints the not-found record, as systemd does.
func (m *Machine) properties(u *Unit) string {
	if u == nil || u.Vanished || u.Reset {
		return notFound
	}

	u.Polls++

	if len(u.Raw) > 0 {
		return u.Raw[min(u.Polls-1, len(u.Raw)-1)]
	}

	if u.Stopped {
		return u.afterStop()
	}

	switch {
	case u.Command == hosttest.UnknownCommand:
		return strings.Replace(Succeeded, "ActiveState=active", "ActiveState=mystery", 1)
	case u.Polls <= max(u.Live, 1):
		return Running
	case u.Command == hosttest.FailingCommand, hosttest.Mangled(u.Command):
		return Failed
	}

	return Succeeded
}

// afterStop is the record of a stopped unit: it still runs for a planned number of reads, then systemd unloads it.
func (u *Unit) afterStop() string {
	if u.Lingers > 0 {
		u.Lingers--

		return Running
	}

	return notFound
}

// The property records of a unit, as the probe measured them.
const (
	Running   = "LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nExecMainCode=0\nExecMainStatus=0\n"
	Succeeded = "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainCode=1\nExecMainStatus=0\n"
	Failed    = "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=3\n"
)
