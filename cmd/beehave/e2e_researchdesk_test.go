package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// researchDeskFakes are the programs of the research desk scripts: the file in testdata and the name in the folder fake of the work folder.
var researchDeskFakes = map[string]string{
	"research-desk-claude.sh": "claude",
	"research-desk-curl.sh":   "curl",
}

// cmdResearchDeskInit is "rdinit": it copies examples/research-desk to the folder rd in the work folder, without its project file,
// and writes the project file to config/projects/research-desk.star. The project has no source, so no repository is made.
// The script then runs the files that users copy, not a second version of them.
func cmdResearchDeskInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: rdinit")
	}

	work := ts.Getenv("WORK")
	desk := filepath.Join(work, "rd")

	if err := os.CopyFS(desk, os.DirFS(filepath.Join("..", "..", "examples", "research-desk"))); err != nil {
		ts.Fatalf("rdinit: %v", err)
	}

	root := openRoot(ts, work, "")
	defer root.Close()

	project, err := root.ReadFile(filepath.Join("rd", "config", "projects", "research-desk.star"))
	if err != nil {
		ts.Fatalf("rdinit: %v", err)
	}

	// The agent steps keep their own sandbox() from the workflow. The program gates run unsandboxed: the staleness gate fetches every cited page.
	text := strings.Replace(string(project), `sandbox = "on"`, `sandbox = "off"`, 1)
	if err := root.WriteFile(filepath.Join("config", "projects", "research-desk.star"), []byte(text), e2eFileMode); err != nil {
		ts.Fatalf("rdinit: %v", err)
	}
}

// cmdResearchDeskFakes is "rdfakes": it writes the fake claude and the stand-in for curl of the research desk scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@. The stand-in for curl stands for the search server, the pages and the wiki.
func cmdResearchDeskFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: rdfakes")
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("rdfakes: %v", err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("rdfakes: %v", err)
	}
	defer testdata.Close()

	for source, name := range researchDeskFakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("rdfakes: %v", err)
		}

		if err := writeFake(ts, filepath.Join("fake", name), text); err != nil {
			ts.Fatalf("rdfakes: %v", err)
		}
	}

	sandboxHome(ts)
}
