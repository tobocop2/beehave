package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// deskFakes are the programs of the desk scripts: the file in testdata and the name in the folder fake of the work folder.
var deskFakes = map[string]string{
	"desk-claude.sh":   "claude",
	"desk-curl.sh":     "curl",
	"desk-sendmail.sh": "sendmail",
}

// cmdDeskInit is "deskinit": it copies examples/desk to the folder desk in the work folder, without its tests, and commits it to a new git repository there.
// It writes the project file of the example to config/projects/desk.star with the source set to that repository.
// The script then runs the files that users copy, not a second version of them.
func cmdDeskInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: deskinit")
	}

	work := ts.Getenv("WORK")
	desk := filepath.Join(work, "desk")

	if err := os.CopyFS(desk, os.DirFS(filepath.Join("..", "..", "examples", "desk"))); err != nil {
		ts.Fatalf("deskinit: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(desk, "tests")); err != nil {
		ts.Fatalf("deskinit: %v", err)
	}

	if err := os.Rename(filepath.Join(desk, "config"), filepath.Join(work, "config-desk")); err != nil {
		ts.Fatalf("deskinit: %v", err)
	}

	root := openRoot(ts, work, "")
	defer root.Close()

	project, err := root.ReadFile(filepath.Join("config-desk", "projects", "desk.star"))
	if err != nil {
		ts.Fatalf("deskinit: %v", err)
	}

	text := strings.Replace(string(project), "~/desk", desk, 1)
	if err := root.WriteFile(filepath.Join("config", "projects", "desk.star"), []byte(text), e2eFileMode); err != nil {
		ts.Fatalf("deskinit: %v", err)
	}

	for _, git := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "-m", "desk"}} {
		command := exec.CommandContext(context.Background(), "git", git...) //nolint:gosec // a test runs git on its own temporary folder
		command.Dir = desk

		if out, err := command.CombinedOutput(); err != nil {
			ts.Fatalf("deskinit: git %s: %v: %s", git[0], err, out)
		}
	}
}

// cmdDeskFakes is "deskfakes": it writes the fake claude, the stand-in for curl and the stand-in for sendmail of the desk scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@. The stand-ins stand for the system of record, the chat service and the mail transfer agent.
func cmdDeskFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: deskfakes")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("deskfakes: %v", err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("deskfakes: %v", err)
	}
	defer testdata.Close()

	for source, name := range deskFakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("deskfakes: %v", err)
		}

		if err := work.WriteFile(filepath.Join("fake", name), []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
			ts.Fatalf("deskfakes: %v", err)
		}
	}
}
