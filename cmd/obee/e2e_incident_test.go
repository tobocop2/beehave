package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// incidentFakes are the programs of the incident scripts: the file in testdata and the name in the folder fake of the work folder.
var incidentFakes = map[string]string{
	"incident-claude.sh": "claude",
	"incident-curl.sh":   "curl",
}

// cmdIncidentInit is "incidentinit": it copies examples/incident to the folder ops in the work folder, without its tests, and commits it to a new git repository there.
// It writes the project file of the example to config/projects/incident.star with the source set to that repository.
// The script then runs the files that users copy, not a second version of them.
func cmdIncidentInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: incidentinit")
	}

	work := ts.Getenv("WORK")
	ops := filepath.Join(work, "ops")

	if err := os.CopyFS(ops, os.DirFS(filepath.Join("..", "..", "examples", "incident"))); err != nil {
		ts.Fatalf("incidentinit: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(ops, "tests")); err != nil {
		ts.Fatalf("incidentinit: %v", err)
	}

	if err := os.Rename(filepath.Join(ops, "config"), filepath.Join(work, "config-incident")); err != nil {
		ts.Fatalf("incidentinit: %v", err)
	}

	root := openRoot(ts, work, "")
	defer root.Close()

	project, err := root.ReadFile(filepath.Join("config-incident", "projects", "incident.star"))
	if err != nil {
		ts.Fatalf("incidentinit: %v", err)
	}

	text := strings.Replace(string(project), "~/ops", ops, 1)
	if err := root.WriteFile(filepath.Join("config", "projects", "incident.star"), []byte(text), e2eFileMode); err != nil {
		ts.Fatalf("incidentinit: %v", err)
	}

	for _, git := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "-m", "ops"}} {
		command := exec.CommandContext(context.Background(), "git", git...) //nolint:gosec // a test runs git on its own temporary folder
		command.Dir = ops

		if out, err := command.CombinedOutput(); err != nil {
			ts.Fatalf("incidentinit: git %s: %v: %s", git[0], err, out)
		}
	}
}

// cmdIncidentFakes is "incidentfakes": it writes the fake claude and the stand-in for curl of the incident scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@. The stand-in for curl stands for the ops API, which is the one part that the example does not ship.
func cmdIncidentFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: incidentfakes")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("incidentfakes: %v", err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("incidentfakes: %v", err)
	}
	defer testdata.Close()

	for source, name := range incidentFakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("incidentfakes: %v", err)
		}

		if err := work.WriteFile(filepath.Join("fake", name), []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
			ts.Fatalf("incidentfakes: %v", err)
		}
	}
}
