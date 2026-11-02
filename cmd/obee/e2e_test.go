package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/review"
	"github.com/tobocop2/obee/internal/workflow/workflowtest"
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
	testscript.Main(m, map[string]func(){"obee": e2eObee, "obeereal": e2eReal})
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
		Client: func(ctx context.Context, _ config.Config, opts flow.Options) (cli.Steering, error) {
			files := e2eFiles{dir: dir}

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: files, Trees: files})
			if err != nil {
				return nil, err
			}

			return client, nil
		},
		Engine: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			files := e2eFiles{dir: dir}

			engine, err := flow.NewEngine(ctx, opts, withBatch(flow.Deps{
				Agents: e2eAgents{dir: dir, path: path}, Gate: e2eGate{dir: dir}, Logins: e2eLogins{},
				Lifecycle: files, Forge: files, Watch: files, Trees: files, Beats: files, Items: files, Preflight: files,
			}, dir, cfg))
			if err != nil {
				return nil, err
			}

			return engine, nil
		},
	}
}

// e2eFiles holds every collaborator that needs no process. It records each lifecycle call in events.log.
type e2eFiles struct{ dir string }

// Fields gives the item the fields that the prompts of the example read.
func (e2eFiles) Fields(_ context.Context, _, item string) (map[string]string, error) {
	return map[string]string{"id": item, "title": "title of " + item, "body": "body of " + item}, nil
}

// Preflight finds nothing changed: the scripts own no pull request before the first agent.
func (e2eFiles) Preflight(context.Context, string, string, preflight.Request, string) (preflight.Result, error) {
	return preflight.Result{}, nil
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

func (e2eFiles) ClosingIssues(context.Context, string, string) ([]int, bool, error) {
	return nil, true, nil
}

func (f e2eFiles) Release(_ context.Context, _, item, _ string) error {
	return f.event("release", item)
}

func (f e2eFiles) Pull(ctx context.Context, _, _ string) (review.Standing, error) {
	head, err := e2eHeadOf(ctx, f.dir)

	return review.Standing{Number: 1, Head: head}, err
}

func (f e2eFiles) Reviewed(ctx context.Context, project, item, _ string) (review.Standing, error) {
	return f.Pull(ctx, project, item)
}

func (e2eFiles) Watch(context.Context, string, int) (review.Outcome, error) {
	return review.Outcome{Kind: review.Merged}, nil
}

func (e2eFiles) SetDraft(context.Context, string, int, bool) error { return nil }

func (e2eFiles) Path(context.Context, string, string) (string, error) { return "", nil }

// Ensure gives the git folder that e2eSetup makes. The gate reads the commit of that folder.
func (f e2eFiles) Ensure(context.Context, string, string) (string, error) {
	return filepath.Join(f.dir, "tree"), nil
}

func (e2eFiles) Beat(context.Context, string, string) (string, error) { return "", nil }

func (e2eFiles) Renewed(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, nil
}

func errJoin(first, second error) error {
	if first != nil {
		return first
	}

	return second
}

// e2eHeadOf reads the commit of the git folder that e2eSetup makes. The gate and the pull request show it.
func e2eHeadOf(ctx context.Context, dir string) (string, error) {
	result := proc.NewExecRunner("git", filepath.Join(dir, "tree")).Run(ctx, []string{"rev-parse", "HEAD"})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git rev-parse: %s", result.Stderr)
	}

	return strings.TrimSpace(result.Stdout), nil
}

// e2eGate passes every item.
type e2eGate struct{ dir string }

func (g e2eGate) Run(ctx context.Context, _, _ string) (flow.GateResult, error) {
	head, err := e2eHeadOf(ctx, g.dir)

	return flow.GateResult{Passed: true, Head: head, Tree: "tree-of-" + head}, err
}

func (g e2eGate) Key(context.Context, string, string) (flow.GateKey, error) {
	return flow.GateKey{}, nil
}

// e2eAgents starts the claude program of the script PATH through the real agent runner.
type e2eAgents struct{ dir, path string }

// It describes the run as the real agents do: the session from the output of the program, the skill calls from the tool_use lines that it printed.
func (a e2eAgents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	prompt := "login=" + request.Login + "\n" + request.Prompt
	env := map[string]string{"PATH": a.path}
	adapter := newAdapters(nil, nil).agent(model.AgentClaude)
	runner := model.Runner{Name: request.Login, Agent: model.AgentClaude, ConfigDir: a.dir}
	tap := newSessionTap(adapter)
	protect := protectionOf(request.Protect, a.dir)

	result := agent.Runner{}.Run(ctx, agent.Spec{
		Adapter: adapter, Runner: runner,
		Dir: a.dir, Prompt: prompt, Env: env, Log: tap, OnStart: request.Started, NoEdits: request.NoEdits,
		Guard: agents{guard: guardCommand(os.Executable)}.guardFor(request.Forbid, protect),
	})

	return describe(adapter, runner, a.dir, tap, result, request.Forbid, protect)
}

// The agent runs in the work folder of the script and sees no other variable than PATH.
// The file hang names the step that blocks on its first call; the file started marks that the block began.
const e2eClaude = `#!/bin/sh
# The fake claude: one line in calls.log for each call. Fields: step round login ruling.
# A prompt that carries a seen value adds it as a line of seen.log.
# A prompt that carries a found value adds its first line to found.log, with the step in front.
# A fix stages everything when the next line of the file fixhits says hit.
# A review writes the next line of the file verdicts to the output file that its prompt names, with no open finding.
# When the file obey-hook exists, the fake obeys --settings as Claude Code does: it runs the hook command of the settings before each Bash call.
# A hook that exits with code 2 blocks the call: the fake adds a line to blocked.log and prints the tool_result of the refusal.
# The implement step also makes one Bash call for each line of the file cmds, when it exists.
# When the file edit-file exists, the implement step writes edited.txt with the Write tool. As with the real claude in print mode,
# the write is refused unless the start arguments hold --permission-mode acceptEdits: the fake adds a line to refused.log and prints the tool_result of the refusal.
hook=
mode=
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then hook=$(printf '%s' "$2" | sed 's/.*"command":"\([^"]*\)".*/\1/'); fi
  if [ "$1" = --permission-mode ]; then mode=$2; fi
  shift
done
write_call() {
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"'"$1"'","name":"Write","input":{"file_path":"'"$2"'"}}]}}'
  if [ "$mode" = acceptEdits ]; then
    echo edited > "$2"
  else
    echo "refused $2" >> refused.log
    echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"'"$1"'","is_error":true,"content":"Claude requested permissions to write to '"$2"', but you haven'"'"'t granted it yet."}]}}'
  fi
}
bash_call() {
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"'"$1"'","name":"Bash","input":{"command":"'"$2"'"}}]}}'
  if [ -e obey-hook ] && [ -n "$hook" ]; then
    out=$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"%s"}}' "$2" | ( eval "$hook" ) 2>&1 >/dev/null)
    code=$?
    if [ "$code" = 2 ]; then
      echo "blocked $2" >> blocked.log
      echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"'"$1"'","is_error":true,"content":"PreToolUse:Bash hook error: '"$(printf '%s\n' "$out" | sed -n 1p)"'"}]}}'
    fi
  fi
}
prompt=$(cat)
field() { printf '%s\n' "$prompt" | sed -n "s/^$1=//p"; }
step=$(field step)
echo "$step $(field round) $(field login) $(field ruling)" >> calls.log
seen=$(field seen)
if [ -n "$seen" ]; then echo "$seen" >> seen.log; fi
found=$(field found)
if [ -n "$found" ]; then echo "$step: $found" >> found.log; fi
if [ -e hang ] && [ "$step" = "$(cat hang)" ] && [ ! -e started ]; then
  : > started
  exec sleep 300
fi
if [ "$step" = review ]; then
  n=$(grep -c '^review ' calls.log)
  printf '{"verdict":"%s","mechanism":"","findings":[]}\n' "$(sed -n "${n}p" verdicts)" > "$(field write)"
fi
# The tool calls arrive as assistant lines of the stream, as with the real claude.
# The fake claude calls the skill that the example requires, so the session reports it.
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"%SKILL%"}}]}}'
# The implementer stages everything, the command that the example forbids.
if [ "$step" = implement ]; then
  if [ -e edit-file ]; then write_call toolu_edit edited.txt; fi
  bash_call toolu_impl "git add -A"
  if [ -e cmds ]; then
    i=0
    while IFS= read -r c; do
      i=$((i + 1))
      bash_call "toolu_cmd$i" "$c"
    done < cmds
  fi
fi
if [ "$step" = fix ]; then
  n=$(grep -c '^fix ' calls.log)
  if [ "$(sed -n "${n}p" fixhits 2>/dev/null)" = hit ]; then
    bash_call "toolu_fix$n" "git add -A"
  fi
fi
echo '{"type":"result","session_id":"s1","result":"done"}'
`

// e2eLogins picks the runner "a" of the config, so no script reads the usage of a login.
type e2eLogins struct{}

func (e2eLogins) Pick(context.Context, string, string, []string, bool) (string, error) {
	return "a", nil
}

// e2ePrompts replace the prompts of the example in the work folder. The workflow file is the example itself.
var e2ePrompts = map[string]string{
	"plan":      "step=plan\n",
	"implement": "step=implement\n",
	"review":    "step=review\nwrite={{ .output_file }}\nseen={{ .violations }}\n",
	"fix":       "step=fix\nround={{ .round }}\nfound={{ .findings }}\n",
	"settle":    "step=settle\nruling={{ .ruling }}\n",
	"audit":     "step=audit\nseen={{ .seen }}\n",
}

const e2eRunners = `agents(max_concurrent = 2)

runner(name = "a", agent = "claude", config_dir = "%DIR%")

runner(name = "b", agent = "claude", config_dir = "%DIR%")
`

const e2eProject = `project(
    repo = "example/shop",
    tracker = "github",
    forge = "github",
    claims = "beads",
    beads_prefix = "shop",
    base_branch = "main",
    gate = "true",
    test_report = "r.xml",
)
`

// exampleSkill returns the one skill that an agent of examples/remediation/workflow.star requires.
func exampleSkill() (string, error) {
	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	if err != nil {
		return "", err
	}

	calls, err := workflowtest.Calls("workflow.star", text)
	if err != nil {
		return "", err
	}

	var skills []string

	for _, call := range calls {
		if call.Builtin == "agent" {
			skills = append(skills, workflowtest.Literals(call.Args["require_skills"])...)
		}
	}

	if len(skills) != 1 {
		return "", fmt.Errorf("the example requires %d skills, want 1", len(skills))
	}

	return skills[0], nil
}

// e2eSetup writes the config folder and the fake claude into the work folder of a script.
func e2eSetup(env *testscript.Env) error {
	work := env.WorkDir
	skill, err := exampleSkill()
	if err != nil {
		return err
	}

	files := map[string]struct {
		text string
		mode os.FileMode
	}{
		"bin/claude":                {strings.ReplaceAll(e2eClaude, "%SKILL%", skill), e2eScriptMode},
		"config/runners.star":       {strings.ReplaceAll(e2eRunners, "%DIR%", work), e2eFileMode},
		"config/projects/shop.star": {e2eProject, e2eFileMode},
	}

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	if err != nil {
		return err
	}

	files["flow/workflow.star"] = struct {
		text string
		mode os.FileMode
	}{string(example), e2eFileMode}

	for name, text := range e2ePrompts {
		files["flow/prompts/"+name+".md"] = struct {
			text string
			mode os.FileMode
		}{text, e2eFileMode}
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

	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, e2eFolderMode); err != nil {
		return err
	}

	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "--allow-empty", "-m", "start"}} {
		if result := proc.NewExecRunner("git", tree).Run(context.Background(), args); result.ExitCode != 0 {
			return fmt.Errorf("git %s: %s", args[0], result.Stderr)
		}
	}

	env.Setenv("PATH", filepath.Join(work, "bin")+string(os.PathListSeparator)+env.Getenv("PATH"))
	env.Setenv("OBEE_CONFIG", filepath.Join(work, "config"))
	env.Setenv("OBEE_STATE_DIR", filepath.Join(work, "state"))
	env.Setenv(e2eDirVariable, work)
	// The example merges only when MERGE is "true". The scripts run it as a dry run: the owner merges, and no merge guard runs.
	env.Setenv("MERGE", "false")
	// The preflight call of the example reads these before the first agent. The fake preflight finds nothing changed, so the values only have to exist.
	env.Setenv("EXPECT_HEAD", "")
	env.Setenv("BASE", "integration")

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
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"waitfile": cmdWaitFile, "waitstatus": cmdWaitStatus,
			"example": cmdExample, "subst": cmdSubst, "isolate": cmdIsolate, "exitcode": cmdExitCode, "noclaims": cmdNoClaims, "mode": cmdMode,
			"batchsetup": cmdBatchSetup, "replace": cmdReplace, "serve": cmdServe, "fakeclaude": cmdFakeClaude,
		},
	})
}
