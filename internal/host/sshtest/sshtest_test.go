package sshtest_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/host/sshtest"
	"github.com/tobocop2/obee/internal/model"
)

// call runs the words on the machine as the link does: options, "--", the target, then each word quoted.
func call(m *sshtest.Machine, words ...string) model.CommandResult {
	args := []string{"-o", "BatchMode=yes", "--", "gates"}
	for _, w := range words {
		args = append(args, model.ShellQuote(w))
	}

	return m.SSH().Run(context.Background(), args)
}

func TestMachine_AnswersTheSmallCommands(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)

	assert.Equal(t, sshtest.User+"\n", call(m, "id", "-un").Stdout)
	assert.Equal(t, sshtest.Root+"\n", call(m, "pwd", "-P").Stdout)
	assert.Equal(t, "Linger=yes\n", call(m, "loginctl", "show-user", "lane", "-p", "Linger").Stdout)
	assert.Equal(t, "running\n", call(m, "systemctl", "--user", "is-system-running").Stdout)
	assert.Equal(t, hosttest.DF, call(m, "df", "-P", "-k", "/x").Stdout)
	assert.Equal(t, "log text\n", call(m, "tail", "-c", "5", "/x").Stdout)
	assert.Contains(t, call(m, "systemd-run", "--version").Stdout, "systemd 255")
	assert.Equal(t, 127, call(m, "nosuch").ExitCode)
	assert.Equal(t, 1, call(m, "systemctl", "--user", "frob").ExitCode)
}

func TestMachine_PlannedFailures(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.LingerOff, m.NoSystemd, m.SystemdText = true, true, "systemd 239"

	assert.Equal(t, "Linger=no\n", call(m, "loginctl").Stdout)
	assert.Equal(t, 1, call(m, "systemctl", "--user", "is-system-running").ExitCode)
	assert.Contains(t, call(m, "systemd-run", "--version").Stdout, "systemd 239")

	m.BadDisk = true
	assert.Contains(t, call(m, "df").Stdout, "cannot read")

	m.NoRun = true
	assert.Equal(t, 1, call(m, "systemd-run", "--user").ExitCode)

	assert.Equal(t, 255, call(sshtest.New(hosttest.Unreachable), "id").ExitCode)
	assert.Equal(t, 1, call(sshtest.New(hosttest.Broken), "id").ExitCode)
	assert.Equal(t, 1, call(m).ExitCode, "a call with no command")
}

func TestMachine_Drops(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)

	m.Drops = 1
	assert.Equal(t, 255, call(m, "id").ExitCode)
	assert.Equal(t, 0, call(m, "id").ExitCode)

	m.DropOn = "pwd"
	assert.Equal(t, 255, call(m, "pwd").ExitCode)
	assert.Equal(t, 0, call(m, "pwd").ExitCode)

	m.DropAll = "df"
	assert.Equal(t, 255, call(m, "df").ExitCode)
	assert.Equal(t, 255, call(m, "df").ExitCode)

	m.Drops = -1
	assert.Equal(t, 255, call(m, "id").ExitCode)
}

func TestMachine_Files(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)

	assert.Equal(t, 1, call(m, "test", "-e", "/d/m").ExitCode)
	call(m, "touch", "/d/m")
	assert.Equal(t, 0, call(m, "test", "-e", "/d/m").ExitCode)
	call(m, "rm", "-f", "/d/m")
	assert.Equal(t, 1, call(m, "test", "-e", "/d/m").ExitCode)

	assert.Equal(t, 0, call(m, "test", "-d", "/anything").ExitCode)
	call(m, "mkdir", "-p", "/d")
	assert.Equal(t, 0, call(m, "test", "-e", "/d").ExitCode)
}

func submit(m *sshtest.Machine, name, command string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	call(m, "systemd-run", "--user", "--unit="+name, "--", "sh", "-c", `eval "$(printf %s `+encoded+` | base64 -d)"`)
}

func observe(m *sshtest.Machine, name string) string {
	return call(m, "sh", "-c", "script", "sh", name, "/b/"+name).Stdout
}

func TestMachine_AUnitReadsRunningThenItsResult(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	submit(m, "u1", hosttest.PassingCommand)
	submit(m, "u2", hosttest.FailingCommand)
	submit(m, "u3", hosttest.UnknownCommand)

	assert.Equal(t, hosttest.PassingCommand, m.Units["u1"].Command)
	assert.Contains(t, observe(m, "u1"), "SubState=running")
	assert.Contains(t, observe(m, "u1"), "SubState=exited")
	assert.Contains(t, observe(m, "u2"), "SubState=running")
	assert.Contains(t, observe(m, "u2"), "ActiveState=failed")
	assert.Contains(t, observe(m, "u3"), "ActiveState=mystery")
}

// A command that did not travel as base64 is not the command that was sent: the machine marks it, so a plain `sh -c <text>` regression is seen.
func TestMachine_AnUndecodableCommandIsMarkedAndNeverEqualToTheText(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	call(m, "systemd-run", "--unit=a", "--", "sh", "-c", "plain")
	call(m, "systemd-run", "--unit=b", "--", "sh", "-c", "printf %s !!!notbase64 | x")
	call(m, "systemd-run", "--unit=c", "--", "sh", "-c", hosttest.HostileCommand)

	assert.Equal(t, sshtest.Undecoded+"plain", m.Units["a"].Command)
	assert.Equal(t, sshtest.Undecoded+"printf %s !!!notbase64 | x", m.Units["b"].Command)
	assert.True(t, hosttest.Mangled(m.Units["c"].Command), "the plain transport of the hostile text is a mangled command")
}

func TestMachine_AnEvalLineOfAnotherShapeIsMarked(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	call(m, "systemd-run", "--unit=a", "--", "sh", "-c", `eval "$(printf %s QQ== | cat)"`)
	call(m, "systemd-run", "--unit=b", "--", "sh", "-c", `eval "$(printf %s !!! | base64 -d)"`)

	assert.True(t, strings.HasPrefix(m.Units["a"].Command, sshtest.Undecoded), "a line that does not decode with base64 -d")
	assert.True(t, strings.HasPrefix(m.Units["b"].Command, sshtest.Undecoded), "text that is not base64")
}

func TestMachine_ARecordedResultIsPrintedByThePoll(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	submit(m, "u1", hosttest.PassingCommand)
	m.Files["/b/u1.timed_out"] = true

	assert.Contains(t, observe(m, "u1"), "obee-result timed_out\n")
	assert.NotContains(t, observe(m, "u1"), "obee-result succeeded")
}

func TestMachine_AHostileCommandThatTravelsAsBase64ArrivesWhole(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	submit(m, "h", hosttest.HostileCommand)

	assert.Equal(t, hosttest.HostileCommand, m.Units["h"].Command)
	observe(m, "h")

	assert.Contains(t, observe(m, "h"), "SubState=exited", "the fake runs a whole hostile command to success")
}

func TestMachine_AMangledCommandEndsFailed(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	call(m, "systemd-run", "--unit=c", "--", "sh", "-c", hosttest.HostileCommand)

	observe(m, "c")

	assert.Contains(t, observe(m, "c"), "ActiveState=failed")
}

func TestMachine_ANeverStartedUnitIsNotFoundAndCarriesTheMarkerAndSize(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.Files["/b/ghost.canceled"] = true

	out := observe(m, "ghost")

	assert.Contains(t, out, "obee-canceled")
	assert.Contains(t, out, "LoadState=not-found")
	assert.True(t, strings.HasSuffix(out, "obee-size -1\n"))
}

func TestMachine_PlannedReadsSizesAndStops(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	submit(m, "r", "x")
	m.Units["r"].Raw = []string{"first\n", "second\n"}
	m.Units["r"].LogSizes = []int{4, 9}

	assert.Equal(t, "first\nobee-size 4\n", observe(m, "r"))
	assert.Equal(t, "second\nobee-size 9\n", observe(m, "r"))
	assert.Equal(t, "second\nobee-size 9\n", observe(m, "r"), "the last read repeats")

	submit(m, "s", "x")
	m.Units["s"].Live = 9
	m.Units["s"].Lingers = 1

	assert.Equal(t, 0, call(m, "systemctl", "--user", "stop", "s").ExitCode)
	assert.Contains(t, observe(m, "s"), "SubState=running", "the unit still runs after the stop")
	assert.Contains(t, observe(m, "s"), "LoadState=not-found")

	assert.Equal(t, 0, call(m, "systemctl", "--user", "reset-failed", "s").ExitCode)
	assert.Equal(t, 5, call(m, "systemctl", "--user", "stop", "s").ExitCode, "a unit that was reset is not loaded")
	assert.Equal(t, 0, call(m, "systemctl", "--user", "reset-failed", "nosuch").ExitCode)
	assert.Equal(t, 5, call(m, "systemctl", "--user", "stop", "nosuch").ExitCode)

	m.NoStop = true
	assert.Equal(t, 1, call(m, "systemctl", "--user", "stop", "r").ExitCode)

	m.Units["v"] = &sshtest.Unit{Vanished: true}
	assert.Contains(t, observe(m, "v"), "not-found")
}

func TestMachine_FailAndSayReplaceTheAnswerOfACommandByItsFirstWords(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.Fail = map[string]int{"test -e": 2}
	m.Say = map[string]string{"id": "other\n"}

	assert.Equal(t, 2, call(m, "test", "-e", "/x").ExitCode)
	assert.Equal(t, 0, call(m, "test", "-d", "/x").ExitCode)
	assert.Equal(t, "other\n", call(m, "id", "-un").Stdout)
}

func TestNew_Modes(t *testing.T) {
	t.Parallel()

	assert.True(t, sshtest.New(hosttest.Broken).Broken)
	assert.True(t, sshtest.New(hosttest.UnreadableDisk).BadDisk)
	assert.True(t, sshtest.New(hosttest.Unreachable).Offline)
	require.NotNil(t, sshtest.New(hosttest.Working).Units)
}
