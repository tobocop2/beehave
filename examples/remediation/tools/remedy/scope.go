package remedy

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/scope"
)

const scopeSyntax = "scope <pr> --state <folder> [--key <key>]"

// scopeCommand refuses a pull request that closes an issue it did not close at its first review, unless the owner approved the issue. A refusal, a bad argument and a failed read are exit code 2.
func scopeCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	request, state, ok := parseScope(args, s.err)
	if !ok {
		return ExitRefused
	}

	result, err := scopeRun(ctx, deps, request, state)
	if err != nil {
		say(s.err, "remedy scope: %v\n", err)

		return ExitRefused
	}

	return showScope(s, result)
}

// showScope prints the lines of the check and gives its exit code.
func showScope(s streams, result scope.Result) int {
	for _, line := range result.Lines {
		say(s.out, "%s\n", line)
	}

	if result.Code != 0 {
		return ExitRefused
	}

	return ExitPass
}

// scopeRun opens the forge of the origin remote and the ledger of the state folder, and checks the pull request.
func scopeRun(ctx context.Context, deps Deps, request scope.Request, state string) (scope.Result, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return scope.Result{}, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return scope.Result{}, err
	}

	request.Repo = repo

	return scope.Check(ctx, deps.Book(state), scope.ReaderOf(forge), request)
}

// parseScope reads the pull request and the flags of scope. It shows the usage and gives false for a bad argument.
func parseScope(args []string, errOut io.Writer) (scope.Request, string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return scope.Request{}, "", scopeUsage(errOut)
	}

	var state, key string

	flags := flag.NewFlagSet("remedy scope", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&state, "state", "", "the state folder that holds the ledger")
	flags.StringVar(&key, "key", "", "the ledger key of the item; the pull request number when not given")

	if flags.Parse(args[1:]) != nil {
		return scope.Request{}, "", false
	}

	if state == "" || flags.NArg() > 0 {
		return scope.Request{}, "", scopeUsage(errOut)
	}

	return scope.Request{PR: args[0], Key: key, Repo: ""}, state, true
}

func scopeUsage(errOut io.Writer) bool {
	say(errOut, "usage: remedy %s\n", scopeSyntax)

	return false
}
