package cli_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

var (
	briefCommandSpan = regexp.MustCompile("`(obee [^`]*)`")
	briefFlag        = regexp.MustCompile(`--[a-z][a-z-]*`)
	usageLine        = regexp.MustCompile(`Usage:\n +(.*)`)
	placeholder      = regexp.MustCompile(`<[^>]*>`)
)

// positionals counts the placeholders of a command span that come after its command words and before its first flag.
func positionals(span string, words int) int {
	args := strings.Fields(span)[1+words:]

	for i, word := range args {
		if strings.ContainsAny(word[:1], "-[") {
			args = args[:i]

			break
		}
	}

	return len(placeholder.FindAllString(strings.Join(args, " "), -1))
}

// commandWords returns the words of a command span that come before its first argument or flag.
func commandWords(span string) []string {
	var words []string

	for _, word := range strings.Fields(strings.TrimPrefix(span, "obee ")) {
		if strings.ContainsAny(word[:1], `<"-[`) {
			break
		}

		words = append(words, word)
	}

	return words
}

// fullTree builds a command tree with every optional command present. The help of a command opens no host and reads no transcript.
func fullTree() cli.BuildInfo {
	return cli.BuildInfo{
		Check: cli.CheckDeps{
			Skills:    func(string, string) ([]model.Call, error) { return nil, nil },
			Bash:      func(string) ([]model.Call, error) { return nil, nil },
			Git:       func(context.Context, []string) model.CommandResult { return model.CommandResult{} },
			Closes:    func(string) ([]string, error) { return nil, nil },
			OwnerAdds: func(string) ([]string, error) { return nil, nil },
			PRBody:    func(context.Context, string, int) (string, error) { return "", nil },
		},
		Host: cli.HostDeps{Open: func(model.Project) (cli.Machine, error) { return nil, errors.New("no host in a help run") }},
	}
}

func TestBriefText_EveryNamedCommandAndFlagExists(t *testing.T) {
	t.Parallel()

	checked := 0

	for _, part := range brief.Parts() {
		text, err := brief.Text(part)
		require.NoError(t, err)

		for _, match := range briefCommandSpan.FindAllStringSubmatch(text, -1) {
			span := strings.Join(strings.Fields(match[1]), " ")
			words := commandWords(span)
			require.NotEmpty(t, words, "%s names no command in %q", part, span)

			code, stdout, stderr := runCLI(t, nil, fullTree(), append(append([]string{}, words...), "--help")...)
			require.Equal(t, 0, code, "%s: `%s` is not a command: %s", part, span, stderr)
			assert.Contains(t, stdout, "obee "+strings.Join(words, " "), "%s: help for `%s` names another command", part, span)

			// A span of command words alone names the command in prose; a span with arguments gives them before its flags.
			if len(strings.Fields(span)) > 1+len(words) {
				usage := usageLine.FindStringSubmatch(stdout)
				require.Len(t, usage, 2, "%s: the help for `%s` has no usage line", part, span)
				assert.Len(t, placeholder.FindAllString(usage[1], -1), positionals(span, len(words)),
					"%s: `%s` gives other arguments than `%s`", part, span, usage[1])
			}

			for _, flag := range briefFlag.FindAllString(span, -1) {
				assert.Contains(t, stdout, flag, "%s: `%s` names a flag that `obee %s` does not have", part, span, strings.Join(words, " "))
			}

			checked++
		}
	}

	assert.Positive(t, checked, "the brief text names no obee command, so the test checks nothing")
}
