package gitexec_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// dumpGit puts a fake git first on PATH that appends its whole environment to the file that the function returns, one block for each call, after a line
// that holds the arguments.
func dumpGit(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"CALL $*\" >> " + log + "\n/usr/bin/env >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

// blocks returns the environment of each call in the log, as the lines of the call.
func blocks(t *testing.T, log string) [][]string {
	t.Helper()

	var found [][]string

	for _, block := range strings.Split(calls(t, log), "CALL ")[1:] {
		found = append(found, strings.Split(strings.TrimSpace(block), "\n"))
	}

	return found
}

func TestRunner_EveryCallStartsFromTheKeptVariablesAndNoConfigOfTheUser(t *testing.T) {
	log := dumpGit(t)
	t.Setenv("GIT_SSH_COMMAND", "evil")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_EXTERNAL_DIFF", "evil")
	t.Setenv("GIT_CONFIG_GLOBAL", "/home/owner/.gitconfig")
	t.Setenv("XDG_CONFIG_HOME", "/home/owner/.config")
	t.Setenv("HOME", "/home/owner")
	t.Setenv("TMPDIR", "/var/tmp")

	widened := gitexec.New(t.TempDir(), 0)
	widened.Env = []string{"GIT_CONFIG_GLOBAL=/home/caller/.gitconfig", "XDG_CONFIG_HOME=/home/caller"}

	gitexec.New(t.TempDir(), 0).Run(t.Context(), []string{"status"})
	widened.Run(t.Context(), []string{"status"})
	cloneRunner(t).Run(t.Context(), []string{"status"})

	found := blocks(t, log)
	require.Len(t, found, 4, "two runs of New, and for the pinned runner the listing of its settings and the run")

	for _, block := range found {
		assert.Subset(t, block, []string{
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME=/dev/null", "GIT_NO_REPLACE_OBJECTS=1",
			"GIT_TERMINAL_PROMPT=0", "HOME=/home/owner", "TMPDIR=/var/tmp",
		}, block[0])

		for _, line := range block[1:] {
			assert.NotContains(t, line, "evil", "no variable of the process outside the kept ones reaches git: %s", block[0])
			assert.NotContains(t, line, "GIT_CONFIG_COUNT", block[0])
			assert.NotContains(t, line, "/home/owner/.gitconfig", block[0])
			assert.NotContains(t, line, "/home/caller", block[0])
		}
	}
}

func TestReadConfig_KeepsTheConfigFilesOfTheOwnerAndNothingElse(t *testing.T) {
	log := dumpGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/home/owner/.gitconfig")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", "/home/owner/.config")
	t.Setenv("GIT_SSH_COMMAND", "evil")
	t.Setenv("GIT_CONFIG_COUNT", "1")

	cloneRunner(t).ReadConfig(t.Context(), "user.name")

	found := blocks(t, log)
	require.Len(t, found, 2, "the listing of the settings and the read")

	read := found[1]
	assert.Subset(t, read, []string{"GIT_CONFIG_GLOBAL=/home/owner/.gitconfig", "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME=/home/owner/.config"})

	for _, line := range read[1:] {
		assert.NotContains(t, line, "evil")
		assert.NotContains(t, line, "GIT_CONFIG_COUNT")
	}

	assert.Subset(t, found[0], []string{"GIT_CONFIG_GLOBAL=/dev/null", "XDG_CONFIG_HOME=/dev/null"}, "the listing of the settings reads no config of the user")
}

// TestTheGitCallsOfThisPackageStartInOnePlace fails when a function of this package other than New builds the runner of a git call, or runs the embedded
// runner without start. Such a call would not start from the kept variables and the empty config of the user.
func TestTheGitCallsOfThisPackageStartInOnePlace(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	var found []string

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)

		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "New" {
				continue
			}

			ast.Inspect(fn, func(node ast.Node) bool {
				if bypassesStart(node) {
					found = append(found, file+": "+fn.Name.Name)
				}

				return true
			})
		}
	}

	assert.Empty(t, found, "build the runner of a git call with New, and start it with start")
}

// bypassesStart reports whether a node builds an ExecRunner, or calls a method of the embedded runner, as in r.ExecRunner.Run(ctx, args).
func bypassesStart(node ast.Node) bool {
	switch typed := node.(type) {
	case *ast.CompositeLit:
		return strings.HasSuffix(typeText(typed.Type), "ExecRunner")
	case *ast.CallExpr:
		call, ok := typed.Fun.(*ast.SelectorExpr)

		return ok && strings.HasSuffix(typeText(call.X), ".ExecRunner")
	}

	return false
}

// typeText returns the dotted name that a type expression holds.
func typeText(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typeText(typed.X) + "." + typed.Sel.Name
	case *ast.Ident:
		return typed.Name
	}

	return ""
}
