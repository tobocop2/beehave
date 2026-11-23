package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/sandbox/landlock"
)

// These tests run the beehave commands as separate processes, as testscript does for any command line tool.
// The collaborators of the workflow that later milestones add are files here; the agent is the real runner and starts a shell script named claude from PATH.

const (
	e2eVersion     = "e2e"
	e2eDirVariable = "E2E_DIR"
	// e2eBuildVariable names the version of the build that beehave reports, so a script can run one state folder under two builds.
	e2eBuildVariable = "E2E_BUILD"
	e2eWait          = 120 * time.Second
	e2ePoll          = 100 * time.Millisecond
	e2eFileMode      = 0o600
	e2eFolderMode    = 0o700
)

func TestMain(m *testing.M) {
	// The test binary acts as the upload-pack helper of the push, as beehave does in main.
	std := helperIO{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	if code, helper := runUploadPack(context.Background(), os.Args, config.ProcessEnv(), machineTools(), std); helper {
		os.Exit(code)
	}

	// The test binary also acts as the Landlock helper, as beehave does in main, for the test that runs a gate under the real enforcer.
	if code, helper := landlock.RunHelper(os.Args, os.Stderr); helper {
		os.Exit(code)
	}

	testscript.Main(m, map[string]func(){"beehave": e2eBeehave, "beehavereal": e2eReal, "beehaveswarm": e2eSwarm, "beehaveround": e2eRound})
}

// e2eBeehave is the beehave command of the scripts. It matches main, with the fake collaborators.
func e2eBeehave() {
	syscall.Umask(privateUmask)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()

	e2eRecordRun(os.Args[1:])

	version := e2eVersion
	if other := env[e2eBuildVariable]; other != "" {
		version = other
	}

	code := cli.Execute(ctx, os.Args[1:], cli.BuildInfo{Version: version, Registry: newAdapters(nil, nil, nil).registry(), Flow: e2eFlow(env[e2eDirVariable], env["PATH"])}, env, os.Stdout, os.Stderr)
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

			engine, err := flow.NewEngine(ctx, withModules(opts), withBatch(flow.Deps{
				Agents: e2eAgents{dir: dir, path: path}, Gate: e2eGate{dir: dir}, Logins: e2eLogins{}, Commands: e2eCommands{dir: dir}, Slots: slotsOf(cfg.Projects), Facts: factsOf(cfg, newAdapters(nil, nil, nil)), SkillLine: skillLineOf(cfg.Runners, newAdapters(nil, nil, nil)),
				Lifecycle: files, Forge: files, Trees: files, Beats: files, Items: files, Preflight: files, Reader: files, Hosts: e2eHosts{files},
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

// Comments gives an item no comments: the pipeline of the example reads the parent of its item.
func (e2eFiles) Comments(context.Context, string, string) ([]string, error) { return nil, nil }

// Parent gives an item no parent.
func (e2eFiles) Parent(context.Context, string, string) (string, error) { return "", nil }

// Up starts no host: the project of the scripts has none. The finish function of the example stops the host that the round started.
func (e2eFiles) Up(context.Context, string, string) (bool, string, error) { return false, "", nil }

// Teardown stops no host.
func (e2eFiles) Teardown(context.Context, string, string) error { return nil }

// e2eHosts is the host keeper of the scripts: the Ready of the lifecycle of e2eFiles has another meaning, so the host keeper answers its own.
type e2eHosts struct{ e2eFiles }

// Ready says no host is ready: the project of the scripts has none.
func (e2eHosts) Ready(context.Context, string, string) (bool, error) { return false, nil }

// LostProof gives no proof: the project of the scripts has no host, so it has no host job.
func (e2eFiles) LostProof(context.Context, string, string, string) (model.LostProof, error) {
	return model.LostProof{}, nil
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

func (e2eFiles) Comment(context.Context, string, string, string, string) (int, error) { return 0, nil }

func (e2eFiles) Open(context.Context, string, string, flow.OpenRequest) (flow.OpenResult, error) {
	// The real forge opens a pull request with a number of 1 or more, and the workflow passes it as own_draft. Standing reads the same pull request.
	return flow.OpenResult{Number: 1, Created: true}, nil
}

func (e2eFiles) Push(context.Context, string, string, string, string, string) error { return nil }

func (e2eFiles) History(context.Context, string, string) ([]model.PriorPull, error) { return nil, nil }

func (f e2eFiles) Release(_ context.Context, _, item, _ string) error {
	return f.event("release", item)
}

func (f e2eFiles) Pull(ctx context.Context, _, _ string) (review.Standing, error) {
	head, err := e2eHeadOf(ctx, f.dir)

	return review.Standing{Number: 1, Head: head}, err
}

func (e2eFiles) WithTree(_ context.Context, _, _ string, fn func(string)) error {
	fn("")

	return nil
}

// Git runs git in the folder that e2eSetup makes, as the clone of a repos folder.
func (e2eFiles) Git(dir string) proc.Runner {
	return gitexec.NewRepositories(filepath.Dir(dir)).Runner(dir, 0)
}

// Heads is never read: WithTree gives no worktree.
func (e2eFiles) Heads(context.Context, string) (string, error) { return "", nil }

// Ensure gives the git folder that e2eSetup makes. The gate reads the commit of that folder.
func (f e2eFiles) Ensure(context.Context, string, string, string, string) (string, error) {
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

// e2eGate passes every item. A gate with a base, the gate of a candidate, also gives a clean check record: one new test, a clean secret scan,
// and a revert run in which the new test fails on the base code.
type e2eGate struct{ dir string }

func (e2eGate) Submit(_ context.Context, _, _, _, base, _, _ string, _ model.Slot) (flow.GateJob, error) {
	return flow.GateJob{Plan: gate.Plan{Base: base}}, nil
}

func (g e2eGate) Run(ctx context.Context, _, _, _ string, job flow.GateJob) (flow.GateResult, error) {
	head, err := e2eHeadOf(ctx, g.dir)
	result := flow.GateResult{Passed: true, Head: head, Tree: "tree-of-" + head}

	if job.Plan.Base != "" {
		result.Checks = &flow.GateChecks{NewTests: []string{"shop TestE2E"}, New: 1, Secrets: new(int), Complete: true, Revert: model.RevertPass, RevertCases: 1}
	}

	return result, err
}

func (g e2eGate) Key(context.Context, string, string, string, string, string) (flow.GateKey, error) {
	return flow.GateKey{}, nil
}

// e2eCommands answers the command calls of the example: the git steps of the checks before review and the mutation job on the host of the project.
// Each call adds its command line to mutation.log. A line of the file mutations is "<command> => <answer>", where the command is the words of the step.
// The Nth call of a command, on any host and slot class, takes the Nth line that names it: "lived" is a failed job that printed one finding, "silent" is a failed job that printed nothing, "clean" is a clean run.
// A call with no line for its command is a clean run, so a new step in the workflow moves no answer. A line with no " => " or an unknown answer fails the call.
type e2eCommands struct{ dir string }

func (c e2eCommands) Run(_ context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	log, err := os.OpenFile(filepath.Join(c.dir, "mutation.log"), os.O_APPEND|os.O_CREATE|os.O_RDWR, e2eFileMode)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	words := strings.Join(request.Run, " ")

	if _, err := log.WriteString(request.Host + " " + request.SlotClass + " " + words + "\n"); err != nil {
		return flow.CommandOutput{}, errJoin(err, log.Close())
	}

	text, err := os.ReadFile(log.Name())
	if err := errJoin(err, log.Close()); err != nil {
		return flow.CommandOutput{}, err
	}

	answers, err := os.ReadFile(filepath.Join(c.dir, "mutations"))
	if err != nil && !os.IsNotExist(err) {
		return flow.CommandOutput{}, err
	}

	answer, err := keyedAnswer(string(answers), words, callsOf(string(text), words))
	if err != nil {
		return flow.CommandOutput{}, err
	}

	switch answer {
	case "lived":
		return flow.CommandOutput{Stdout: "      KILLED CONDITIONALS_NEGATION at internal/a.go:30:4\nfinding: internal/a.go:12: a mutant lived: CONDITIONALS_NEGATION\n", RC: 1}, nil
	case "silent":
		return flow.CommandOutput{RC: 1}, nil
	}

	return flow.CommandOutput{}, nil
}

// callsOf is the number of lines of the log whose command words equal words, on any host and slot class. A log line is "<host> <slot class> <words>".
func callsOf(log, words string) int {
	n := 0

	for _, line := range strings.Split(log, "\n") {
		if fields := strings.SplitN(line, " ", 3); len(fields) == 3 && fields[2] == words {
			n++
		}
	}

	return n
}

// e2eAnswers are the answers that a line of the file mutations may give.
var e2eAnswers = []string{"clean", "lived", "silent"}

// keyedAnswer returns the answer of the nth call of a command: the nth line of the answers whose key is exactly the words. It is empty when no such line exists.
// A non-blank line with no " => ", or with an answer that is not in e2eAnswers, is an error that names the line.
func keyedAnswer(answers, words string, nth int) (string, error) {
	var keyed []string

	for number, line := range strings.Split(answers, "\n") {
		if line == "" {
			continue
		}

		key, answer, found := strings.Cut(line, " => ")
		if !found {
			return "", fmt.Errorf("mutations line %d %q has no \" => \"", number+1, line)
		}

		if !slices.Contains(e2eAnswers, answer) {
			return "", fmt.Errorf("mutations line %d %q gives the unknown answer %q, want one of %v", number+1, line, answer, e2eAnswers)
		}

		if key == words {
			keyed = append(keyed, answer)
		}
	}

	if nth < 1 || nth > len(keyed) {
		return "", nil
	}

	return keyed[nth-1], nil
}

// e2eAgents starts the claude program of the script PATH through the real agent runner.
type e2eAgents struct{ dir, path string }

// It describes the run as the real agents do: the session from the output of the program, the skill calls from the tool_use lines that it printed.
// A start after a lost run resumes its session or adds the note, as the real agents decide it.
func (a e2eAgents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	adapter := newAdapters(nil, nil, nil).agent(claude.Name)
	runner := model.Runner{Name: request.Login, Agent: claude.Name, ConfigDir: a.dir}
	request, resumed := replayOf(request, runner.ConfigDir, a.dir, adapter)
	prompt := "login=" + request.Login + "\n" + request.Prompt
	env := map[string]string{"PATH": a.path}
	tap := newSessionTap(adapter, agent.DefaultPrices(), request.Named)
	protect := protectionOf(request.Protect, a.dir)

	result := agent.Runner{}.Run(ctx, agent.Spec{
		Adapter: adapter, Runner: runner, ConfigFolder: a.dir,
		Dir: a.dir, Prompt: prompt, Env: env, Log: tap, OnStart: request.Started, NoEdits: request.NoEdits, Resume: request.Resume,
		Guard: agents{guard: guardCommand(os.Executable)}.guardFor(request.Forbid, protect),
	})

	result, err := endOf(adapter, runner.ConfigDir, a.dir, tap, result, request.Forbid, protect)
	result.Resumed = resumed

	return result, err
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
# When the file edit-file exists, the implement step writes edited.txt with the Write tool. As with the documented mode acceptEdits of the real claude
# in a run with no sandbox, the write is refused unless the start arguments hold --permission-mode acceptEdits: the fake adds a line to refused.log and prints the tool_result of the refusal.
# When the file hang-session exists, the call that blocks first names the session of that file in an init line and writes the transcript of the session:
# the prompt, under the transcript folder of the login. A call with --resume <id> adds its prompt to resumed.log.
# A resumed call whose prompt names no step (the continue prompt) reads the prompt of the session from its transcript, as the real claude continues a session that holds its brief.
# Each resumed call then adds "<step> --resume <id>" to resumes.log, with the flag and the id as it got them.
hook=
mode=
resume=
resumed=
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then
    case "$2" in *'"command":"'*) hook=$(printf '%s' "$2" | sed 's/.*"command":"\([^"]*\)".*/\1/') ;; esac
  fi
  if [ "$1" = --permission-mode ]; then mode=$2; fi
  if [ "$1" = --resume ]; then resume=$2; resumed="$1 $2"; fi
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
if [ -n "$resume" ]; then
  printf '%s\n' "$prompt" >> resumed.log
  if [ -z "$(field step)" ]; then prompt=$(cat "%TRANSCRIPTS%/$resume.jsonl"); fi
  echo "$(field step) $resumed" >> resumes.log
fi
step=$(field step)
echo "$step $(field round) $(field login) $(field ruling)" >> calls.log
seen=$(field seen)
if [ -n "$seen" ]; then echo "$seen" >> seen.log; fi
found=$(field found)
if [ -n "$found" ]; then echo "$step: $found" >> found.log; fi
# A prompt that carries a found value also adds all its lines, from that value to the end, to findings.log.
if [ -n "$found" ]; then printf '%s\n' "$prompt" | sed -n '/^found=/,$p' >> findings.log; fi
if [ -e hang ] && [ "$step" = "$(cat hang)" ] && [ ! -e started ]; then
  if [ -e hang-session ]; then
    session=$(cat hang-session)
    echo '{"type":"system","subtype":"init","session_id":"'"$session"'"}'
    mkdir -p "%TRANSCRIPTS%"
    printf '%s\n' "$prompt" > "%TRANSCRIPTS%/$session.jsonl"
  fi
  : > started
  exec sleep 300
fi
if [ "$step" = review ]; then
  n=$(grep -c '^review ' calls.log)
  printf '{"verdict":"%s","mechanism":"","regressions":"0","open":"","findings":"[]"}\n' "$(sed -n "${n}p" verdicts)" > "$(field write)"
fi
if [ "$step" = plan ]; then
  printf '{"regressions":"0"}\n' > "$(field write)"
fi
if [ "$step" = settle ]; then
  printf '{"parked":"[]"}\n' > "$(field write)"
fi
# A lane answers the two outputs the example declares for it: its report and the reds it did not cause. The report check of the scripts is answered by e2eCommands.
# A fix also answers the two outputs of the review ledger: it resolved no id and declares no finding wontfix.
if [ "$step" = implement ]; then
  printf '{"report":"# Report\\n","uncaused":"[]"}\n' > "$(field write)"
fi
if [ "$step" = fix ]; then
  printf '{"report":"# Report\\n","uncaused":"[]","resolved":"[]","wontfix":"[]"}\n' > "$(field write)"
fi
# The sweep step answers its report output and records the first line of the prompt of the example, the line that loads the skill. The line before it is the login line of this harness.
if [ "$step" = sweep ]; then
  printf '{"report":"# Report\\n"}\n' > "$(field write)"
  printf '%s\n' "$prompt" | sed -n 2p >> sweepline.log
fi
# The tool calls arrive as assistant lines of the stream, as with the real claude.
# The fake claude calls the skill that the example requires, so the session reports it.
# When the file no-skill exists, the fake reports no skill call, as a CLI that does not report them.
# The result of the call follows on a user line: with the file unknown-skill, the error that the CLI gives for a skill it did not load.
skill=%SKILL%
if [ ! -e no-skill ]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"skill":"'"$skill"'"}}]}}'
  if [ -e unknown-skill ]; then
    echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_skill","is_error":true,"content":"<tool_use_error>Unknown skill: '"$skill"'</tool_use_error>"}]}}'
  else
    echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_skill","content":"Launching skill: '"$skill"'"}]}}'
  fi
fi
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
echo '{"type":"result","session_id":"'"${resume:-s1}"'","result":"done"}'
`

// e2eLogins picks the runner "a" of the config, so no script reads the usage of a login.
type e2eLogins struct{}

func (e2eLogins) Pick(context.Context, string, string, []string, bool) (string, error) {
	return "a", nil
}

func (e2eLogins) Reset(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (e2eLogins) Headroom(context.Context) bool { return false }

// e2ePrompts replace the prompts of the example in the work folder. The workflow file is the example itself.
var e2ePrompts = map[string]string{
	"plan":      "step=plan\nwrite={{ .output_file }}\n",
	"implement": "step=implement\nwrite={{ .output_file }}\n",
	"sweep":     "step=sweep\nwrite={{ .output_file }}\n",
	"review":    "step=review\nwrite={{ .output_file }}\nseen={{ .violations }}\n",
	"fix":       "step=fix\nround={{ .round }}\nwrite={{ .output_file }}\nfound={{ .findings }}\n",
	"settle":    "step=settle\nwrite={{ .output_file }}\nruling={{ .ruling }}\n",
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
    beads = beads(prefix = "shop"),
    base_branch = "main",
    gate = "true",
    test_report = "r.xml",
    sandbox = "off",
    build_host = "build",
    host = host(provider = "ssh", disk_floor_gb = 50, slots = {"mutation": 1}, ssh = ssh(address = "10.0.0.5")),
    vars = {
        "round": {"base": "integration", "owner": "owner", "lane_cap": 1, "fix_rounds": 5, "source_checkout": "/own/checkout", "label": "round"},
        "mutation_command": ["sh", "mutation.sh"],
        "review_skill": "` + e2eSkill + `",
    },
)
`

// e2eSkill is the skill that the project of the scripts names in vars.review_skill and that the fake claude calls.
const e2eSkill = "the-review-skill"

// exampleMain is the one entry of the example that the scripts replace.
const exampleMain = "def main(item):"

// oneItemCopy returns the text of the example for the scripts that run one item: its main is renamed round_main and a new main runs pipeline.
// The scripts prove the engine in a real process over the pipeline. A full round needs the queue, remedy, a base branch and a host.
func oneItemCopy(example string) (string, error) {
	if found := strings.Count(example, exampleMain); found != 1 {
		return "", fmt.Errorf("the example holds %d lines %q, want 1", found, exampleMain)
	}

	return strings.Replace(example, exampleMain, "def round_main(item):", 1) + "\n" + exampleMain + "\n    return pipeline(item)\n", nil
}

// e2eFile is a file of the work folder of a script; a program is a file that the script starts.
type e2eFile struct {
	text    string
	program bool
}

// writeE2EFiles writes each file below work, with the folders it needs.
func writeE2EFiles(work string, files map[string]e2eFile) error {
	for name, file := range files {
		path := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(path), e2eFolderMode); err != nil {
			return err
		}

		if err := writeE2EFile(path, file); err != nil {
			return err
		}
	}

	return nil
}

func writeE2EFile(path string, file e2eFile) error {
	if file.program {
		return exectest.Write(path, file.text)
	}

	return os.WriteFile(path, []byte(file.text), e2eFileMode)
}

// e2eSetup writes the config folder and the fake claude into the work folder of a script.
func e2eSetup(env *testscript.Env) error {
	work := env.WorkDir

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	if err != nil {
		return err
	}

	// The fake claude keeps a transcript where the adapter looks for it: the agent of the scripts runs in the work folder with the work folder as its login folder.
	transcripts := filepath.Dir(newAdapters(nil, nil, nil).agent(claude.Name).TranscriptPath(work, work, "session"))
	fake := strings.NewReplacer("%SKILL%", e2eSkill, "%TRANSCRIPTS%", transcripts).Replace(e2eClaude)

	files := map[string]e2eFile{
		"bin/claude":                {fake, true},
		"config/runners.star":       {strings.ReplaceAll(e2eRunners, "%DIR%", work), false},
		"config/projects/shop.star": {e2eProject, false},
	}

	copied, err := oneItemCopy(string(example))
	if err != nil {
		return err
	}

	files["flow/workflow.star"] = e2eFile{copied, false}

	for name, text := range e2ePrompts {
		files["flow/prompts/"+name+".md"] = e2eFile{text, false}
	}

	if err := writeE2EFiles(work, files); err != nil {
		return err
	}

	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, e2eFolderMode); err != nil {
		return err
	}

	// The remote branch of the round base points at the first commit, as in a clone, so the checks before review find a head that descends from the base.
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "--allow-empty", "-m", "start"},
		{"update-ref", "refs/remotes/origin/integration", "HEAD"},
	} {
		if result := proc.NewExecRunner("git", tree).Run(context.Background(), args); result.ExitCode != 0 {
			return fmt.Errorf("git %s: %s", args[0], result.Stderr)
		}
	}

	env.Setenv("PATH", filepath.Join(work, "bin")+string(os.PathListSeparator)+env.Getenv("PATH"))
	env.Setenv("BEEHAVE_CONFIG", filepath.Join(work, "config"))
	env.Setenv("BEEHAVE_STATE_DIR", filepath.Join(work, "state"))
	env.Setenv(e2eDirVariable, work)

	return nil
}

// cmdWaitFile is "waitfile <path>": it waits until the file exists.
func cmdWaitFile(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitfile <path>")
	}

	path := ts.MkAbs(args[0])
	e2eRunWait(ts, neg, e2eWaitingFor(ts, path, func() bool {
		_, err := os.Stat(path)

		return err == nil
	}))
}

// cmdWaitSession is "waitsession <id>": it waits until the log of the engine, flow.log in the state folder, says that the engine saved the session id.
// The engine saves the id on a goroutine of its own after the agent names it, so a kill that only waits for the agent could come before the save.
func cmdWaitSession(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitsession <id>")
	}

	e2eRunWait(ts, neg, e2eWaitingFor(ts, "the engine to save the session "+args[0], func() bool {
		text, err := os.ReadFile(filepath.Join(ts.Getenv("BEEHAVE_STATE_DIR"), "flow.log"))

		return err == nil && slices.ContainsFunc(strings.Split(string(text), "\n"), func(line string) bool {
			return strings.Contains(line, `msg="saved the session of the agent"`) && strings.HasSuffix(line, " session="+args[0])
		})
	}))
}

// cmdWaitLine is "waitline <path> <text>": it waits until a line of the file holds the text. A run that a script starts with its stderr in a file
// writes the start of each step there, so a kill can wait for a step of one run when the file of an agent of another run comes first.
func cmdWaitLine(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 2 {
		ts.Fatalf("usage: waitline <path> <text>")
	}

	path := ts.MkAbs(args[0])
	e2eRunWait(ts, neg, e2eWaitingFor(ts, "a line of "+path+" to hold "+args[1], func() bool {
		text, err := os.ReadFile(filepath.Clean(path))

		return err == nil && slices.ContainsFunc(strings.Split(string(text), "\n"), func(line string) bool { return strings.Contains(line, args[1]) })
	}))
}

// cmdWaitStatus is "waitstatus <text>": it runs beehave status until the output holds the text.
func cmdWaitStatus(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitstatus <text>")
	}

	e2eRunWait(ts, neg, e2eWaitingFor(ts, "beehave status to show "+args[0], func() bool {
		out, err := e2eStatus(ts)

		return err == nil && strings.Contains(out, args[0])
	}))
}

func TestE2E_TheOneItemCopyHasOneMain(t *testing.T) {
	t.Parallel()

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	require.NoError(t, err)

	copied, err := oneItemCopy(string(example))
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(copied, exampleMain))
	assert.Contains(t, copied, "def round_main(item):")
	assert.True(t, strings.HasSuffix(copied, exampleMain+"\n    return pipeline(item)\n"))
}

// e2eCommandCall runs the command words once on the host and slot class, with the mutations file text, and returns the exit code.
func e2eCommandCall(t *testing.T, dir, host, slots string, run ...string) (int, error) {
	t.Helper()

	out, err := e2eCommands{dir: dir}.Run(t.Context(), flow.CommandRequest{Run: run, Host: host, SlotClass: slots})

	return out.RC, err
}

func TestE2ECommands_CountsTheCallsOfTheSameWordsAcrossHosts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mutations"), []byte("sh m.sh => silent\nsh m.sh => clean\n"), e2eFileMode))

	first, err := e2eCommandCall(t, dir, "build", "mutation", "sh", "m.sh")
	require.NoError(t, err)
	second, err := e2eCommandCall(t, dir, "other", "", "sh", "m.sh")
	require.NoError(t, err)

	require.Equal(t, 1, first, "the first call takes the first line")
	require.Equal(t, 0, second, "the same words on another host take the second line")
}

func TestE2ECommands_AKeyThatIsAPrefixMatchesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mutations"), []byte("sh m.sh => silent\ngit => silent\n"), e2eFileMode))

	longer, err := e2eCommandCall(t, dir, "", "", "sh", "m.sh", "origin/integration")
	require.NoError(t, err)
	git, err := e2eCommandCall(t, dir, "", "", "git", "status")
	require.NoError(t, err)

	require.Equal(t, 0, longer, "a key that is a prefix of the words gives no answer")
	require.Equal(t, 0, git, "a key that is a prefix of one word gives no answer")
}

func TestE2ECommands_RefusesAMutationsLineThatIsNotAnAnswer(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"line_without_the_arrow": {"clean\n", `mutations line 1 "clean" has no " => "`},
		"arrow_without_spaces":   {"sh m.sh => clean\nsh m.sh=>lived\n", `mutations line 2 "sh m.sh=>lived" has no " => "`},
		"unknown_answer":         {"sh m.sh => lvied\n", `mutations line 1 "sh m.sh => lvied" gives the unknown answer "lvied"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "mutations"), []byte(test.text), e2eFileMode))

			_, err := e2eCommandCall(t, dir, "", "", "git", "status")

			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestE2E_TheOneItemCopyRefusesAnExampleWithTwoMains(t *testing.T) {
	t.Parallel()

	_, err := oneItemCopy("def main(item):\n    pass\ndef main(item):\n    pass\n")

	require.ErrorContains(t, err, "holds 2 lines")
}

func TestScripts(t *testing.T) {
	t.Parallel()

	tools := writeTools(t)

	testscript.Run(t, testscript.Params{
		Dir:   "testdata/e2e",
		Setup: e2eSetup,
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"waitfile": cmdWaitFile, "waitstatus": cmdWaitStatus, "waitsession": cmdWaitSession, "waitline": cmdWaitLine,
			"example": cmdExample, "script": cmdScript, "subst": cmdSubst, "isolate": isolateFunc(tools), "exitcode": cmdExitCode, "noclaims": cmdNoClaims, "mode": cmdMode,
			"swarmfakes": cmdSwarmFakes, "batchsetup": cmdBatchSetup, "replace": cmdReplace, "serve": cmdServe, "fakeclaude": cmdFakeClaude, "deskfakes": cmdDeskFakes, "deskinit": cmdDeskInit,
			"incidentfakes": cmdIncidentFakes, "incidentinit": cmdIncidentInit, "maintfakes": cmdMaintFakes, "maintinit": cmdMaintInit,
			"rdinit": cmdResearchDeskInit, "rdfakes": cmdResearchDeskFakes,
			"pipeinit": cmdPipelineInit, "pipefakes": cmdPipelineFakes,
			"approvalsfakes": cmdApprovalsFakes, "approvalsinit": cmdApprovalsInit, "approvalskeys": cmdApprovalsKeys,
			"reviewfakes": cmdReviewLoopFakes, "claudemodel": cmdClaudeModel,
		},
	})
}
