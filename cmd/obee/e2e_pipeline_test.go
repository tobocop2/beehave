package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// pipelineFakes are the programs of the ticket pipeline scripts: the file in testdata and the name in the folder fake of the work folder.
var pipelineFakes = map[string]string{
	"pipeline-claude.sh": "claude",
	"pipeline-curl.sh":   "curl",
}

// cmdPipelineInit is "pipeinit": it copies examples/ticket-pipeline to the folder pl in the work folder
// and writes its project file to config/projects/pipeline.star. The project has no source, so no repository is made.
// The script then runs the files that users copy, not a second version of them.
func cmdPipelineInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: pipeinit")
	}

	work := ts.Getenv("WORK")

	if err := os.CopyFS(filepath.Join(work, "pl"), os.DirFS(filepath.Join("..", "..", "examples", "ticket-pipeline"))); err != nil {
		ts.Fatalf("pipeinit: %v", err)
	}

	root := openRoot(ts, work, "")
	defer root.Close()

	project, err := root.ReadFile(filepath.Join("pl", "config", "projects", "pipeline.star"))
	if err != nil {
		ts.Fatalf("pipeinit: %v", err)
	}

	if err := root.WriteFile(filepath.Join("config", "projects", "pipeline.star"), project, e2eFileMode); err != nil {
		ts.Fatalf("pipeinit: %v", err)
	}
}

// cmdPipelineFakes is "pipefakes": it writes the fake claude and the stand-in for curl of the ticket pipeline scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@. The stand-in for curl stands for the ticket source, the one part that the example does not ship.
func cmdPipelineFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: pipefakes")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("pipefakes: %v", err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("pipefakes: %v", err)
	}
	defer testdata.Close()

	for source, name := range pipelineFakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("pipefakes: %v", err)
		}

		if err := work.WriteFile(filepath.Join("fake", name), []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
			ts.Fatalf("pipefakes: %v", err)
		}
	}
}
