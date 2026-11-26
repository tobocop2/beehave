package realtools_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/proc"
)

// env reads one variable of the process environment through internal/config, the only package that reads it.
func env(name string) string { return config.ProcessEnv()[name] }

// pinnedGh is the version of gh that the table of the contract is read from.
const pinnedGh = "2.101.0"

// sandboxRepo is the repository that the token rows read and write, unless BEEHAVE_REAL_TOOLS_REPO names another one.
const sandboxRepo = "tobocop2/beehave-real-tools"

// requireSuite skips the test unless make test-real-tools started it.
func requireSuite(t *testing.T) {
	t.Helper()

	if env("BEEHAVE_REAL_TOOLS") != "1" {
		t.Skip("BEEHAVE_REAL_TOOLS is not 1: run make test-real-tools")
	}
}

// requireToken skips the test unless the suite runs and GH_TOKEN is set.
func requireToken(t *testing.T) {
	t.Helper()
	requireSuite(t)

	if env("GH_TOKEN") == "" {
		t.Skip("GH_TOKEN is not set: this row reads or writes the sandbox repository and needs a token")
	}
}

// sandbox returns the repository of the token rows.
func sandbox() string {
	if repo := env("BEEHAVE_REAL_TOOLS_REPO"); repo != "" {
		return repo
	}

	return sandboxRepo
}

// ghOffline returns a runner of the pinned gh with an empty configuration folder and no login. A call that reads help or the version needs no network.
func ghOffline(t *testing.T) proc.ExecRunner {
	t.Helper()

	run := proc.NewExecRunner("gh", "")
	run.Env = []string{"GH_CONFIG_DIR=" + t.TempDir(), "GH_TOKEN=x", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}

	return run
}

// help is what a command prints for --help: its flags and its JSON fields.
type help struct {
	flags  map[string]bool
	fields []string
}

var (
	sectionLine = regexp.MustCompile(`^[A-Z][A-Za-z ]*:?$`)
	flagLine    = regexp.MustCompile(`^(?:  (?:(-[A-Za-z]), )?|      )(--[A-Za-z0-9-]+)(.*)$`)
	typedFlag   = regexp.MustCompile(`^ [^ ]`)
)

// parseHelp reads the help text of gh. A flag maps to true when it takes a value: its name is followed by a type word.
func parseHelp(text string) help {
	parsed := help{flags: map[string]bool{}}
	section, list := "", ""

	for _, line := range strings.Split(text, "\n") {
		if sectionLine.MatchString(line) {
			section = strings.TrimSuffix(strings.ToUpper(line), ":")

			continue
		}

		switch match := flagLine.FindStringSubmatch(line); {
		case strings.HasSuffix(section, "FLAGS") && match != nil:
			takes := typedFlag.MatchString(match[3])
			parsed.flags[match[2]] = takes

			if match[1] != "" {
				parsed.flags[match[1]] = takes
			}
		case section == "JSON FIELDS" && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			list += strings.TrimSpace(line)
		}
	}

	if list != "" {
		parsed.fields = strings.Split(strings.ReplaceAll(list, " ", ""), ",")
	}

	slices.Sort(parsed.fields)

	return parsed
}

// helpOf runs gh with the words and --help, and parses the answer. A command of the gh-stack extension skips the test when the extension is not installed.
func helpOf(t *testing.T, run proc.ExecRunner, words []string) help {
	t.Helper()

	done := run.Run(t.Context(), append(slices.Clone(words), "--help"))
	if done.ExitCode != 0 && words[0] == "stack" {
		t.Skipf("gh %s has no help: the gh-stack extension is not installed on this host (owed: row L4)", strings.Join(words, " "))
	}

	require.Zero(t, done.ExitCode, "gh %s --help: %s", strings.Join(words, " "), done.Stderr)

	return parseHelp(done.Stdout + done.Stderr)
}
