package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

// These tests run the obee commands as separate processes, as testscript does for any command line tool.
// The collaborators of the workflow that later milestones add are files here; the agent is the real runner and starts a shell script named claude from PATH.

const (
	e2eVersion     = "e2e"
	e2eDirVariable = "E2E_DIR"
	e2eWait        = 60 * time.Second
	e2ePoll        = 100 * time.Millisecond
	e2eFileMode    = 0o600
	e2eScriptMode  = 0o700
	e2eFolderMode  = 0o700
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){"obee": e2eObee})
}

// e2eObee is the obee command of the scripts. It matches main, with the fake collaborators.
func e2eObee() {
	syscall.Umask(privateUmask)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()
	code := cli.Execute(ctx, os.Args[1:], cli.BuildInfo{Version: e2eVersion, Flow: e2eFlow(env[e2eDirVariable], env["PATH"])}, env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// e2eFlow keeps the real client and gives the engine the fake collaborators.
func e2eFlow(dir, path string) cli.Flow {
	return cli.Flow{
		Client: func(ctx context.Context, opts flow.Options) (cli.Steering, error) {
			files := e2eFiles{dir: dir}

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: files, Trees: files})
			if err != nil {
				return nil, err
			}

			return client, nil
		},
		Engine: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			files := e2eFiles{dir: dir}

			engine, err := flow.NewEngine(ctx, opts, flow.Deps{
				Briefs: files, Agents: e2eAgents{dir: dir, path: path}, Gate: e2eGate{}, Verdicts: files, Logins: newLogins(cfg),
				Lifecycle: files, Forge: files, Watch: files, Trees: files, Beats: files,
			})
			if err != nil {
				return nil, err
			}

			return engine, nil
		},
	}
}

// e2eFiles holds every collaborator that needs no process. It records each lifecycle call in events.log.
type e2eFiles struct{ dir string }

func (f e2eFiles) Write(_ context.Context, request flow.BriefRequest) (string, error) {
	folder := filepath.Join(f.dir, "briefs")
	if err := os.MkdirAll(folder, e2eFolderMode); err != nil {
		return "", err
	}

	text := strings.Join([]string{
		"step=" + string(request.Step), "round=" + strconv.Itoa(request.Round), "findings=" + request.Findings, "answer=" + request.Answer, "",
	}, "\n")
	path := filepath.Join(folder, string(request.Step)+"-"+strconv.Itoa(request.Round)+".txt")

	return path, os.WriteFile(path, []byte(text), e2eFileMode)
}

// Read returns the verdict that the review agent left in the verdict file.
func (f e2eFiles) Read(_ context.Context, _, _ string, round int) (flow.Review, error) {
	text, err := os.ReadFile(filepath.Join(f.dir, "verdict"))
	if err != nil {
		return flow.Review{}, err
	}

	verdict := flow.Verdict(strings.TrimSpace(string(text)))
	if verdict != flow.VerdictApprove && verdict != flow.VerdictChanges {
		return flow.Review{}, fmt.Errorf("unknown verdict %q", verdict)
	}

	return flow.Review{Verdict: verdict, Findings: "findings of review " + strconv.Itoa(round)}, nil
}

func (f e2eFiles) event(name, item string) error {
	log, err := os.OpenFile(filepath.Join(f.dir, "events.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, e2eFileMode)
	if err != nil {
		return err
	}

	_, err = log.WriteString(name + " " + item + "\n")

	return errJoin(err, log.Close())
}

func (f e2eFiles) Claim(_ context.Context, _, item string) (string, error) {
	return "owner", f.event("claim", item)
}

func (f e2eFiles) Ready(_ context.Context, _, item string) error { return f.event("ready", item) }

func (f e2eFiles) Release(_ context.Context, _, item, _ string) error {
	return f.event("release", item)
}

func (e2eFiles) Pull(context.Context, string, string) (review.Standing, error) {
	return review.Standing{Number: 1}, nil
}

func (e2eFiles) Watch(context.Context, int) (review.Outcome, error) {
	return review.Outcome{Kind: review.Merged}, nil
}

func (e2eFiles) SetDraft(context.Context, int, bool) error { return nil }

func (e2eFiles) Path(context.Context, string, string) (string, error) { return "", nil }

func (e2eFiles) Beat(context.Context, string, string) (string, error) { return "", nil }

func errJoin(first, second error) error {
	if first != nil {
		return first
	}

	return second
}

// e2eGate passes every item.
type e2eGate struct{}

func (e2eGate) Run(context.Context, string, string) (flow.GateResult, error) {
	return flow.GateResult{Passed: true}, nil
}

// e2eAgents starts the claude program of the script PATH through the real agent runner.
type e2eAgents struct{ dir, path string }

func (a e2eAgents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	brief, err := os.ReadFile(request.Brief)
	if err != nil {
		return agent.Result{}, err
	}

	prompt := "login=" + request.Login + "\nfresh=" + strconv.FormatBool(request.Fresh) + "\n" + string(brief)
	env := map[string]string{"PATH": a.path}

	return agent.Runner{}.Run(ctx, agent.Spec{
		Adapter: newAdapters(nil).agent(model.AgentClaude),
		Runner:  model.Runner{Name: request.Login, Agent: model.AgentClaude, ConfigDir: a.dir},
		Dir:     a.dir, Prompt: prompt, Env: env, Log: io.Discard, OnStart: request.Started,
	}), nil
}

// The agent runs in the work folder of the script and sees no other variable than PATH.
// The file hang names the step that blocks on its first call; the file started marks that the block began.
const e2eClaude = `#!/bin/sh
# The fake claude: one line in calls.log for each call. Fields: step round fresh login answer.
prompt=$(cat)
field() { printf '%s\n' "$prompt" | sed -n "s/^$1=//p"; }
step=$(field step)
echo "$step $(field round) $(field fresh) $(field login) $(field answer)" >> calls.log
if [ -e hang ] && [ "$step" = "$(cat hang)" ] && [ ! -e started ]; then
  : > started
  exec sleep 300
fi
if [ "$step" = review ]; then
  n=$(grep -c '^review ' calls.log)
  sed -n "${n}p" verdicts > verdict
fi
echo '{"type":"result"}'
`

const e2eRunners = `[agents]
max_concurrent = 2

[[runner]]
name = "a"
agent = "claude"
config_dir = "%DIR%"

[[runner]]
name = "b"
agent = "claude"
config_dir = "%DIR%"
`

const e2eProject = `repo = "example/shop"
tracker = "github"
forge = "github"
beads_prefix = "shop"
base_branch = "main"
gate = "true"

[steps.implement]
runner = "a"
fresh_runner = "b"
`

// e2eSetup writes the config folder and the fake claude into the work folder of a script.
func e2eSetup(env *testscript.Env) error {
	work := env.WorkDir
	files := map[string]struct {
		text string
		mode os.FileMode
	}{
		"bin/claude":                {e2eClaude, e2eScriptMode},
		"config/runners.toml":       {strings.ReplaceAll(e2eRunners, "%DIR%", work), e2eFileMode},
		"config/projects/shop.toml": {e2eProject, e2eFileMode},
	}

	for name, file := range files {
		path := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(path), e2eFolderMode); err != nil {
			return err
		}

		if err := os.WriteFile(path, []byte(file.text), file.mode); err != nil {
			return err
		}
	}

	env.Setenv("PATH", filepath.Join(work, "bin")+string(os.PathListSeparator)+env.Getenv("PATH"))
	env.Setenv("OBEE_CONFIG", filepath.Join(work, "config"))
	env.Setenv("OBEE_STATE_DIR", filepath.Join(work, "state"))
	env.Setenv(e2eDirVariable, work)

	return nil
}

// waitFor polls until done reports true. It fails the script after e2eWait.
func waitFor(ts *testscript.TestScript, what string, done func() bool) {
	for deadline := time.Now().Add(e2eWait); time.Now().Before(deadline); time.Sleep(e2ePoll) {
		if done() {
			return
		}
	}

	ts.Fatalf("gave up waiting for %s", what)
}

// cmdWaitFile is "waitfile <path>": it waits until the file exists.
func cmdWaitFile(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitfile <path>")
	}

	path := ts.MkAbs(args[0])
	waitFor(ts, path, func() bool {
		_, err := os.Stat(path)

		return err == nil
	})
}

// cmdWaitStatus is "waitstatus <text>": it runs obee status until the output holds the text.
func cmdWaitStatus(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitstatus <text>")
	}

	var binary string

	for _, folder := range filepath.SplitList(ts.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(folder, "obee")); err == nil {
			binary = filepath.Join(folder, "obee")

			break
		}
	}

	waitFor(ts, "obee status to show "+args[0], func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
		defer cancel()

		command := exec.CommandContext(ctx, binary, "status")
		command.Env = []string{
			"OBEE_CONFIG=" + ts.Getenv("OBEE_CONFIG"), "OBEE_STATE_DIR=" + ts.Getenv("OBEE_STATE_DIR"), e2eDirVariable + "=" + ts.Getenv(e2eDirVariable),
			"PATH=" + ts.Getenv("PATH"), "HOME=" + ts.Getenv("HOME"),
		}
		out, err := command.Output()

		return err == nil && strings.Contains(string(out), args[0])
	})
}

func TestScripts(t *testing.T) {
	t.Parallel()

	testscript.Run(t, testscript.Params{
		Dir:   "testdata/e2e",
		Setup: e2eSetup,
		Cmds:  map[string]func(*testscript.TestScript, bool, []string){"waitfile": cmdWaitFile, "waitstatus": cmdWaitStatus},
	})
}
