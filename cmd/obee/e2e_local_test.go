package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store/local"
)

// The scripts of the local example run obeereal. It is the command that main builds, the real composition root, and it differs in two ways:
// the version is e2e, and the pool pick reads a usage of zero for every runner, because a fake claude has no login to ask.
// The agent is the fake claude program of the script, which the real agent runner starts from PATH.

const (
	e2eToolMode   = 0o700
	e2eWorkMarker = "@WORK@"
	e2eModeBits   = 32
)

// e2eTools are the programs that the scripts of the examples find on PATH. Nothing else is there: no bd and no gh.
// The local example needs sh, git, sed, mkdir and make. The check and report scripts of the research example also use grep, sort, cat, dirname, curl and date.
func e2eTools() []string {
	return []string{"sh", "git", "sed", "mkdir", "make", "grep", "sort", "cat", "dirname", "curl", "date", "awk", "tr", "head", "wc", "mv", "cut", "sleep", "uniq", "comm", "cmp", "basename", "cp"}
}

// e2eOptionalTools are the programs that only some scripts need. A machine without one still runs the other scripts, and a script that needs one
// starts with the condition [!exec:name] skip. The desk example needs jq for its gateway and eval scripts, and a SHA-256 tool for its mask:
// sha256sum on Linux and shasum on macOS. The approvals example also needs ssh-keygen for the signatures, sqlite3 for the ledger and flock for its lock.
func e2eOptionalTools() []string {
	return []string{"jq", "sha256sum", "shasum", "ssh-keygen", "sqlite3", "flock"}
}

// e2eReal is the obeereal command of the scripts.
func e2eReal() {
	// The umask is open, so a file is private only when the code asks for it. main sets privateUmask, and this proves the explicit mode under it.
	syscall.Umask(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()
	idle := func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) }
	factory := flowFactoryFrom(func(ctx context.Context, cfg config.Config, opts flow.Options) (*flow.Engine, error) {
		return newEngineReading(ctx, cfg, opts, idle)
	})
	code := cli.Execute(ctx, os.Args[1:], buildInfo(env, e2eVersion, factory), env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// cmdExample is "example <path> [<to>]": it copies examples/<path> of the repository to <to> (default <path>) in the work folder.
// The script then runs the file that users copy, not a second version of it.
func cmdExample(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) < 1 || len(args) > 2 {
		ts.Fatalf("usage: example <path> [<to>]")
	}

	to := args[len(args)-1]

	examples := openRoot(ts, filepath.Join("..", ".."), "examples")
	defer examples.Close()

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	text, err := examples.ReadFile(args[0])
	if err != nil {
		ts.Fatalf("example: %v", err)
	}

	if err := work.MkdirAll(filepath.Dir(to), e2eFolderMode); err != nil {
		ts.Fatalf("example: %v", err)
	}

	if err := work.WriteFile(to, text, e2eFileMode); err != nil {
		ts.Fatalf("example: %v", err)
	}
}

// cmdReplace is "replace <file> <old> <new>": it replaces the one text old in the file, and fails when the file does not hold it.
func cmdReplace(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 3 {
		ts.Fatalf("usage: replace <file> <old> <new>")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	text, err := work.ReadFile(args[0])
	if err != nil {
		ts.Fatalf("replace: %v", err)
	}

	if !strings.Contains(string(text), args[1]) {
		ts.Fatalf("replace: %s does not hold %q", args[0], args[1])
	}

	if err := work.WriteFile(args[0], []byte(strings.Replace(string(text), args[1], args[2], 1)), e2eFileMode); err != nil {
		ts.Fatalf("replace: %v", err)
	}
}

// openRoot opens the folder below parent. The script reads and writes only inside it.
func openRoot(ts *testscript.TestScript, parent, folder string) *os.Root {
	root, err := os.OpenRoot(filepath.Join(parent, folder))
	if err != nil {
		ts.Fatalf("open the folder %s: %v", folder, err)
	}

	return root
}

// cmdSubst is "subst <file>...": it replaces @WORK@ in each file with the work folder of the script.
func cmdSubst(ts *testscript.TestScript, _ bool, args []string) {
	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	for _, name := range args {
		text, err := work.ReadFile(name)
		if err != nil {
			ts.Fatalf("subst: %v", err)
		}

		if err := work.WriteFile(name, []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eFileMode); err != nil {
			ts.Fatalf("subst: %v", err)
		}
	}
}

// isolateFunc is the "isolate" command over the folder of the wrappers that writeTools made.
// The wrappers are written once, before any script runs. A script that wrote them itself held a write descriptor open while a parallel script
// forked, and Linux refuses to run a file that a forked child still holds open for writing (text file busy).
func isolateFunc(tools string) func(*testscript.TestScript, bool, []string) {
	return func(ts *testscript.TestScript, _ bool, _ []string) { cmdIsolate(ts, tools) }
}

// writeTools writes one wrapper for each of e2eTools into a new folder of t and returns the folder.
func writeTools(t *testing.T) string {
	t.Helper()

	tools := t.TempDir()

	for _, name := range append(e2eTools(), e2eOptionalTools()...) {
		found, err := exec.LookPath(name)
		if err != nil && slices.Contains(e2eOptionalTools(), name) {
			continue
		}

		require.NoError(t, err)

		wrapper := "#!/bin/sh\nexec " + found + " \"$@\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte(wrapper), e2eToolMode))
	}

	return tools
}

// cmdIsolate sets PATH to the fake claude of the script (the folder fake), the obee commands and the wrappers in tools.
// A wrapper runs the program of the machine by its full path, so the script sees git and sh and no other tool of the machine.
func cmdIsolate(ts *testscript.TestScript, tools string) {
	work := ts.Getenv("WORK")

	var commands string

	for _, folder := range filepath.SplitList(ts.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(folder, "obeereal")); err == nil {
			commands = folder

			break
		}
	}

	if commands == "" {
		ts.Fatalf("isolate: obeereal is not on PATH")
	}

	ts.Setenv("PATH", strings.Join([]string{filepath.Join(work, "fake"), commands, tools}, string(os.PathListSeparator)))
}

// cmdExitCode is "exitcode <code> <command> [args...]": it runs the command and fails unless it exits with the code.
// The output stays for the stdout and stderr commands that follow.
func cmdExitCode(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) < 2 {
		ts.Fatalf("usage: exitcode <code> <command> [args...]")
	}

	want, err := strconv.Atoi(args[0])
	if err != nil {
		ts.Fatalf("exitcode: %v", err)
	}

	err = ts.Exec(args[1], args[2:]...)

	var exit *exec.ExitError

	switch {
	case err == nil && want != 0:
		ts.Fatalf("exitcode: %s exited 0, want %d", args[1], want)
	case err != nil && !errors.As(err, &exit):
		ts.Fatalf("exitcode: %v", err)
	case exit != nil && exit.ExitCode() != want:
		ts.Fatalf("exitcode: %s exited %d, want %d", args[1], exit.ExitCode(), want)
	}
}

// cmdNoClaims is "noclaims <project>": it fails when local.db in the state folder holds a claim row with a holder for the project.
func cmdNoClaims(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: noclaims <project>")
	}

	ctx := context.Background()

	store, err := local.Open(ctx, ts.Getenv("OBEE_STATE_DIR"), args[0], time.Now)
	if err != nil {
		ts.Fatalf("noclaims: %v", err)
	}

	rows, err := store.HeldRows(ctx)
	if err != nil {
		ts.Fatalf("noclaims: %v", err)
	}

	if err := store.Close(); err != nil {
		ts.Fatalf("noclaims: %v", err)
	}

	if len(rows) != 0 {
		ts.Fatalf("noclaims: project %s holds %d claim rows: %v", args[0], len(rows), rows)
	}
}

// cmdMode is "mode <path> <octal>": it fails unless the file has the permission bits.
func cmdMode(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 2 {
		ts.Fatalf("usage: mode <path> <octal>")
	}

	want, err := strconv.ParseUint(args[1], 8, e2eModeBits)
	if err != nil {
		ts.Fatalf("mode: %v", err)
	}

	info, err := os.Stat(ts.MkAbs(args[0]))
	if err != nil {
		ts.Fatalf("mode: %v", err)
	}

	if got := info.Mode().Perm(); got != os.FileMode(want) {
		ts.Fatalf("mode: %s is %s, want %s", args[0], fmt.Sprintf("%04o", uint32(got)), args[1])
	}
}
