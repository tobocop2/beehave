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

// approvalsFakes are the programs of the approval scripts, in the same form.
var approvalsFakes = map[string]string{
	"approvals-claude.sh":  "claude",
	"approvals-curl.sh":    "curl",
	"approvals-signoff.sh": "signoff",
}

// cmdApprovalsKeys is "approvalskeys <name>...": it makes an ssh key for each name in the folder keys of the work folder, with ssh-keygen,
// and writes the file home/.approval/allowed_signers with the public keys, as the desk expects them (the namespace beehave-signoff).
// A name that starts with outsider- gets a key but no line in the file.
func cmdApprovalsKeys(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) == 0 {
		ts.Fatalf("usage: approvalskeys <name>...")
	}

	work := ts.Getenv("WORK")
	keys := filepath.Join(work, "keys")

	if err := os.MkdirAll(keys, e2eFolderMode); err != nil {
		ts.Fatalf("approvalskeys: %v", err)
	}

	var signers strings.Builder

	for _, name := range args {
		path := filepath.Join(keys, name)

		generate := exec.CommandContext(context.Background(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", path) //nolint:gosec // a test runs ssh-keygen on its own temporary folder
		if out, err := generate.CombinedOutput(); err != nil {
			ts.Fatalf("approvalskeys: ssh-keygen: %v: %s", err, out)
		}

		public, err := os.ReadFile(filepath.Clean(path + ".pub"))
		if err != nil {
			ts.Fatalf("approvalskeys: %v", err)
		}

		if strings.HasPrefix(name, "outsider-") {
			continue // a key that the desk does not know
		}

		signers.WriteString(name + " namespaces=\"beehave-signoff\" " + strings.TrimSpace(string(public)) + "\n")
	}

	desk := filepath.Join(work, "home", ".approval")
	if err := os.MkdirAll(desk, e2eFolderMode); err != nil {
		ts.Fatalf("approvalskeys: %v", err)
	}

	if err := os.WriteFile(filepath.Join(desk, "allowed_signers"), []byte(signers.String()), e2eFileMode); err != nil {
		ts.Fatalf("approvalskeys: %v", err)
	}
}

// cmdIncidentInit is "incidentinit": it copies examples/incident to the folder ops in the work folder, without its tests, and commits it to a new git repository there.
// It writes the project file of the example to config/projects/incident.star with the source set to that repository.
// The script then runs the files that users copy, not a second version of them.
func cmdIncidentInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: incidentinit")
	}

	initExample(ts, "incidentinit", "incident")
}

// cmdApprovalsInit is "approvalsinit": the same for examples/approvals and config/projects/approvals.star.
func cmdApprovalsInit(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: approvalsinit")
	}

	initExample(ts, "approvalsinit", "approvals")
}

func initExample(ts *testscript.TestScript, command, example string) {
	work := ts.Getenv("WORK")
	ops := filepath.Join(work, "ops")

	if err := os.CopyFS(ops, os.DirFS(filepath.Join("..", "..", "examples", example))); err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	if err := os.RemoveAll(filepath.Join(ops, "tests")); err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	if err := os.Rename(filepath.Join(ops, "config"), filepath.Join(work, "config-"+example)); err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	root := openRoot(ts, work, "")
	defer root.Close()

	project, err := root.ReadFile(filepath.Join("config-"+example, "projects", example+".star"))
	if err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	text := strings.Replace(string(project), "~/"+exampleSource(example), ops, 1)
	if err := root.WriteFile(filepath.Join("config", "projects", example+".star"), []byte(text), e2eFileMode); err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	for _, git := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "-m", "ops"}} {
		run := exec.CommandContext(context.Background(), "git", git...) //nolint:gosec // a test runs git on its own temporary folder
		run.Dir = ops

		if out, err := run.CombinedOutput(); err != nil {
			ts.Fatalf("%s: git %s: %v: %s", command, git[0], err, out)
		}
	}
}

// exampleSource is the source path that the project file of an example holds before the test replaces it.
func exampleSource(example string) string {
	if example == "incident" {
		return "ops"
	}

	return example
}

// cmdIncidentFakes is "incidentfakes": it writes the fake claude and the stand-in for curl of the incident scripts to the folder fake in the work folder.
// Each program holds the work folder in place of @WORK@. The stand-in for curl stands for the ops API, which is the one part that the example does not ship.
func cmdIncidentFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: incidentfakes")
	}

	writeFakes(ts, "incidentfakes", incidentFakes)
}

// cmdApprovalsFakes is "approvalsfakes": the same for the approval scripts. The stand-in for curl stands for the notifier, which the example does not ship.
func cmdApprovalsFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: approvalsfakes")
	}

	writeFakes(ts, "approvalsfakes", approvalsFakes)
}

func writeFakes(ts *testscript.TestScript, command string, fakes map[string]string) {
	work := openRoot(ts, ts.Getenv("WORK"), "")
	defer work.Close()

	if err := work.MkdirAll("fake", e2eFolderMode); err != nil {
		ts.Fatalf("%s: %v", command, err)
	}

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		ts.Fatalf("%s: %v", command, err)
	}
	defer testdata.Close()

	for source, name := range fakes {
		text, err := testdata.ReadFile(source)
		if err != nil {
			ts.Fatalf("%s: %v", command, err)
		}

		if err := work.WriteFile(filepath.Join("fake", name), []byte(strings.ReplaceAll(string(text), e2eWorkMarker, ts.Getenv("WORK"))), e2eToolMode); err != nil {
			ts.Fatalf("%s: %v", command, err)
		}
	}
}
