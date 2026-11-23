package agenttest

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/exectest"
)

const (
	// modeCLI prints the last argument, the standard input, and for a last argument that is a file, the content, the mode and the path of the file.
	modeCLI = "#!/bin/sh\nfor last; do :; done\necho \"last=$last\"\necho \"stdin=$(cat)\"\n" +
		"if [ -f \"$last\" ]; then echo \"file=$(cat \"$last\")\"; echo \"filemode=$(ls -l \"$last\" | cut -c1-10)\"; echo \"filepath=$last\"; fi\n"
	// wrapCLI is a prefix: it prints its first two arguments, then starts the rest of the line.
	wrapCLI = "#!/bin/sh\necho \"wrapper=$1|$2\"\nshift 2\nexec \"$@\"\n"
	// wrapName is the program of the prefix, and its two arguments are one word each. The first has a space, to show that no shell splits it.
	wrapName = "wrap"
	wrapOne  = "one word"
	wrapTwo  = "two"
)

// delivery is an adapter whose launch also says how the prompt arrives and what comes before the CLI.
type delivery struct {
	agent.Adapter
	prompt agent.PromptMode
	prefix []string
}

func (d delivery) Guard(command, runDir string, write, skills []string) agent.Launch {
	launch := d.Adapter.Guard(command, runDir, write, skills)
	launch.Prompt, launch.Prefix = d.prompt, d.prefix

	return launch
}

// CheckPrompt starts a fake CLI in each way that the launch can send the prompt (stdin, argument and file) and with a prefix, and checks that the runner does
// what the launch says. The prompt in an argument is one argument. The file has mode 0600, holds the prompt, and is gone when the run ends. Each word of the
// prefix is one argument. The checks are of the runner, with the arguments of the profile and the launch of the adapter in front, so a launch that already
// sets a mode or a prefix is replaced for them.
func CheckPrompt(r Reporter, adapter agent.Adapter, dir string) {
	r.Helper()

	for _, program := range [][2]string{{adapter.Profile().Binary, modeCLI}, {wrapName, wrapCLI}} {
		if err := exectest.Write(filepath.Join(dir, program[0]), program[1]); err != nil {
			r.Errorf("cannot write the fake program %s: %v", program[0], err)

			return
		}
	}

	stdin := seenWith(adapter, dir, agent.PromptStdin, nil)
	expect(r, slices.Contains(stdin, "stdin="+prompt) && !slices.Contains(stdin, "last="+prompt), "a launch with the mode stdin did not send the prompt on stdin only: %q", stdin)

	argument := seenWith(adapter, dir, agent.PromptArgument, nil)
	expect(r, slices.Contains(argument, "last="+prompt) && slices.Contains(argument, "stdin="), "a launch with the mode argument did not send the prompt as the last argument only: %q", argument)

	checkPromptFile(r, adapter, dir)
	checkPrefix(r, adapter, dir)
}

// checkPrefix checks that a launch with a prefix starts the CLI after the words of the prefix, each word as one argument.
func checkPrefix(r Reporter, adapter agent.Adapter, dir string) {
	r.Helper()

	prefixed := seenWith(adapter, dir, agent.PromptStdin, []string{wrapName, wrapOne, wrapTwo})
	expect(r, slices.Contains(prefixed, "wrapper="+wrapOne+"|"+wrapTwo) && slices.Contains(prefixed, "stdin="+prompt),
		"a launch with a prefix did not start the CLI after the words of the prefix, each as one argument: %q", prefixed)
}

// checkPromptFile checks the mode file: the prompt is in a file that only the user reads, the file is the last argument, and the run removes it.
func checkPromptFile(r Reporter, adapter agent.Adapter, dir string) {
	r.Helper()

	file := seenWith(adapter, dir, agent.PromptFile, nil)
	path := valueOf(file, "filepath=")

	expect(r, slices.Contains(file, "file="+prompt) && slices.Contains(file, "stdin=") && path != "", "a launch with the mode file did not put the prompt in the last argument as a file only: %q", file)
	expect(r, slices.Contains(file, "filemode=-rw-------"), "the prompt file of a launch with the mode file is not private to the user: %q", file)

	_, err := os.Stat(path)
	expect(r, path != "" && os.IsNotExist(err), "the prompt file %q is still there after the run", path)
}

// seenWith runs the fake CLI with the launch that has the mode and the prefix, and returns the lines that it printed.
func seenWith(adapter agent.Adapter, dir string, mode agent.PromptMode, prefix []string) []string {
	var out bytes.Buffer

	runFake(delivery{Adapter: adapter, prompt: mode, prefix: prefix}, dir, &out)

	return strings.Split(out.String(), "\n")
}

// valueOf returns what follows prefix on the first line that starts with it, or "".
func valueOf(lines []string, prefix string) string {
	for _, line := range lines {
		if value, ok := strings.CutPrefix(line, prefix); ok {
			return value
		}
	}

	return ""
}
