package main

import (
	"context"

	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	forgegithub "github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/ledger"
	"github.com/tobocop2/obee/internal/proc"
)

// checkDeps fills every read of the check commands from the real adapters.
// The ledger and the forge open on each call, so a command that never reads them needs neither the state folder nor a repo.
func checkDeps(env map[string]string) cli.CheckDeps {
	return cli.CheckDeps{
		Skills:    claude.Adapter{}.SkillCalls,
		Bash:      claude.Adapter{}.BashCalls,
		Git:       proc.NewExecRunner("git", "").Run,
		Closes:    func(key string) ([]string, error) { return fromBook(env, key, (*ledger.Ledger).Closes) },
		OwnerAdds: func(key string) ([]string, error) { return fromBook(env, key, (*ledger.Ledger).OwnerAdds) },
		PRBody:    prBody,
	}
}

// fromBook opens the ledger and reads a list of lines from it for a key.
func fromBook(env map[string]string, key string, read func(*ledger.Ledger, string) ([]string, error)) ([]string, error) {
	dir, err := config.StateDir(env)
	if err != nil {
		return nil, err
	}

	return read(ledger.Open(dir), key)
}

// prBody opens the forge of the repo and reads the body of a pull request.
func prBody(ctx context.Context, repo string, pr int) (string, error) {
	gh, err := forgegithub.NewExec(repo)
	if err != nil {
		return "", err
	}

	return gh.PRBody(ctx, pr)
}
