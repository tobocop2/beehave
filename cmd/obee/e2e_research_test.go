package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// serverFile is the file in the work folder that holds the address of the server of "serve".
const serverFile = "server.url"

// cmdFakeClaude is "fakeclaude": it writes the fake claude of the research scripts, testdata/research-claude.sh, to fake/claude in the work folder.
// The program holds the work folder in place of @WORK@. All the research scripts share this one program.
func cmdFakeClaude(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: fakeclaude")
	}

	text, err := os.ReadFile(filepath.Join("testdata", "research-claude.sh"))
	if err != nil {
		ts.Fatalf("fakeclaude: %v", err)
	}

	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("fakeclaude: %v", err)
	}

	if err := work.WriteFile("fake/claude", []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
		ts.Fatalf("fakeclaude: %v", err)
	}
}

// cmdServe is "serve": it starts a server on the loopback address and writes its address into the file server.url of the work folder.
// The path /ok answers with status 200 and every other path answers with status 404. The server stops when the script ends.
// The fake claude of the research scripts reads the file to write a source that answers.
func cmdServe(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: serve")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ok" {
			http.NotFound(w, r)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	ts.Defer(server.Close)

	if err := os.WriteFile(filepath.Join(ts.Getenv("WORK"), serverFile), []byte(server.URL), e2eFileMode); err != nil {
		ts.Fatalf("serve: %v", err)
	}
}
