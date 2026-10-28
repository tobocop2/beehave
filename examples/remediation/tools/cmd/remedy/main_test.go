package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/proc"
)

// These tests run the remedy tool as a separate process, as testscript does for any command line tool.
// The tool of the scripts is the real entry function with no forge, so the scripts run it with no build and no PATH setup.

const treeFolderMode = 0o700

var errNoForge = errors.New("the scripts have no forge")

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){"remedy": scriptRemedy})
}

func scriptRemedy() {
	os.Exit(remedy.Main(os.Args[1:], os.Stdout, os.Stderr, func(string) (remedy.Forge, error) { return nil, errNoForge }, nil))
}

// scriptSetup makes the git folder `tree` in the work folder of a script, with one empty commit.
func scriptSetup(env *testscript.Env) error {
	tree := filepath.Join(env.WorkDir, "tree")
	if err := os.MkdirAll(tree, treeFolderMode); err != nil {
		return err
	}

	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "--allow-empty", "-m", "start"}} {
		if result := proc.NewExecRunner("git", tree).Run(context.Background(), args); result.ExitCode != 0 {
			return fmt.Errorf("git %s: %s", args[0], result.Stderr)
		}
	}

	return nil
}

func TestScripts(t *testing.T) {
	t.Parallel()

	testscript.Run(t, testscript.Params{Dir: "testdata", Setup: scriptSetup})
}
