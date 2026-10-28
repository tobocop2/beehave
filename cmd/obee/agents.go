package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

var (
	errNoRunner  = errors.New("the runners file has no runner of this name")
	errNoFolder  = errors.New("the item has no worktree to run the agent in")
	errNoPrompt  = errors.New("the agent request has no prompt")
	errNoSession = errors.New("the agent left no session id in its output")
)

// agents runs an agent request with the adapter of the runner it names.
type agents struct {
	runners []model.Runner
	trees   treeFinder
	adapter func(model.Agent) agent.Adapter
	env     map[string]string
	run     func(ctx context.Context, spec agent.Spec) agent.Result
	skills  func(adapter agent.Adapter, path string) ([]model.Call, error)
}

// newAgents runs the agent CLIs with the environment of the process.
func newAgents(cfg config.Config, opts flow.Options, trees treeFinder, table adapters) agents {
	return agents{
		runners: cfg.Runners,
		trees:   trees,
		adapter: table.agent,
		env:     opts.Env,
		run:     func(ctx context.Context, spec agent.Spec) agent.Result { return agent.Runner{}.Run(ctx, spec) },
		skills:  func(adapter agent.Adapter, path string) ([]model.Call, error) { return adapter.SkillCalls(path, "") },
	}
}

// Run starts the agent of the login in the worktree of the item and returns what it left: its session, its last message and the skills it called.
func (a agents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	runner, dir, err := a.target(ctx, request)
	if err != nil {
		return agent.Result{}, err
	}

	adapter := a.adapter(runner.Agent)
	tap := &sessionTap{adapter: adapter}

	result := a.run(ctx, agent.Spec{
		Adapter: adapter, Runner: runner, Dir: dir, Prompt: request.Prompt, Env: a.env,
		Timeout: request.Timeout, IdleTimeout: request.IdleTimeout, Model: request.Model, Resume: request.Resume,
		Log: tap, OnStart: request.Started,
	})

	return a.describe(adapter, runner, dir, tap, result)
}

// target returns the runner of the login and the worktree of the item. A request with no prompt, an unknown login or no worktree is refused.
func (a agents) target(ctx context.Context, request flow.AgentRequest) (model.Runner, string, error) {
	if request.Prompt == "" {
		return model.Runner{}, "", fmt.Errorf("%w: item %s", errNoPrompt, request.Item)
	}

	runner, found := a.runnerOf(request.Login)
	if !found {
		return model.Runner{}, "", fmt.Errorf("%w: %q", errNoRunner, request.Login)
	}

	dir, err := a.trees.Path(ctx, request.Project, request.Item)
	if err != nil {
		return model.Runner{}, "", fmt.Errorf("find the folder of item %s: %w", request.Item, err)
	}

	if dir == "" {
		return model.Runner{}, "", fmt.Errorf("%w: item %s of project %s", errNoFolder, request.Item, request.Project)
	}

	return runner, dir, nil
}

func (a agents) runnerOf(name string) (model.Runner, bool) {
	for _, runner := range a.runners {
		if runner.Name == name {
			return runner, true
		}
	}

	return model.Runner{}, false
}

// describe adds the session, the transcript, the last message and the skill calls to the result of a run that exited with code 0.
func (a agents) describe(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result) (agent.Result, error) {
	if result.Outcome != agent.OutcomeExited || result.ExitCode != 0 {
		return result, nil
	}

	result.Session, result.Text = tap.found()
	if result.Session == "" {
		return result, errNoSession
	}

	result.Transcript = adapter.TranscriptPath(runner.ConfigDir, dir, result.Session)

	var err error

	result.Skills, err = a.skillNames(adapter, result.Transcript)
	if err != nil {
		return result, fmt.Errorf("read the skill calls of session %s: %w", result.Session, err)
	}

	return result, nil
}

// skillNames returns the name of each skill the transcript shows.
func (a agents) skillNames(adapter agent.Adapter, path string) ([]string, error) {
	calls, err := a.skills(adapter, path)

	var names []string
	for _, call := range calls {
		names = append(names, call.Text)
	}

	return names, err
}

// sessionTap is the log of a run. It feeds each output line to the adapter and keeps the last session id and message it finds.
// The runner writes one whole line at a time, so no line is split across two writes.
type sessionTap struct {
	adapter agent.Adapter
	mu      sync.Mutex
	session string
	text    string
}

func (t *sessionTap) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, line := range bytes.Split(data, []byte("\n")) {
		session, text := t.adapter.Line(string(line))
		if session != "" {
			t.session = session
		}

		if text != "" {
			t.text = text
		}
	}

	return len(data), nil
}

func (t *sessionTap) found() (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.session, t.text
}
