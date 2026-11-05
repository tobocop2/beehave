// Package agent starts coding-agent CLIs. An Adapter supplies the profile of one CLI and reads its output stream.
package agent

import "time"

// Profile is the command line and the login variables of one agent CLI. The prompt always goes on stdin.
// ModelFlag is the flag that takes a model name. An empty flag means the CLI has no such option.
type Profile struct {
	Binary string
	Args   []string
	// EditArgs follow Args unless the spec sets NoEdits. They let the CLI edit files in the work folder.
	EditArgs []string
	// NoEditArgs and NoEditEnv are for a CLI that edits files unless told not to. The runner adds NoEditArgs after Args and NoEditEnv after the allow-list
	// when the spec sets NoEdits, and adds neither otherwise. A launch cannot change a variable of NoEditEnv.
	NoEditArgs []string
	NoEditEnv  map[string]string
	ConfigVars []string
	TokenVar   string
	ModelFlag  string
	// Resume returns the arguments that resume the session with the given id. A nil Resume means the CLI has no such option.
	// ResumeLast is true when those arguments open a subcommand, so the runner puts them after every other argument.
	Resume     func(id string) []string
	ResumeLast bool
	// TurnsFlag and BudgetFlag are the flags that cap the turns and the dollars of one run. An empty flag means the CLI has no such option.
	// A set turn cap on a profile without TurnsFlag is enforced by the runner, which counts the shell calls of the run (the tool steps) as turns.
	// A set cost cap on a profile without BudgetFlag refuses the run, because the runner learns the cost of a run too late to stop it,
	// unless StreamsCost is true: the CLI then reports the dollars of each step on its output (Seen.Spent), and the runner ends the run with OutcomeBudgetCap
	// when their sum passes the cap. The step in flight is not counted yet, so a run can pass the cap by the cost of one step.
	// A capped run of such a CLI that reports no dollars at all ends as OutcomeBudgetCap when it exits, because the cap was not checked; it never counts as zero.
	TurnsFlag   string
	BudgetFlag  string
	StreamsCost bool
	// Sandbox is true when the CLI has a sandbox that the adapter maps a SandboxPlan to. A run with a sandbox on an adapter without it does not start.
	// MinVersion is the oldest version of the CLI that knows every setting of that mapping; an older CLI ignores a setting it does not know.
	Sandbox    bool
	MinVersion string
	// GuardMinVersion is the oldest version of the CLI whose hook of Guard is known to load. A CLI that does not load the hook runs every shell command
	// with no check and exits with code 0, so a run with a guard on an older or unreadable version does not start. Empty means no check.
	GuardMinVersion string
	// GuardPosixOnly is true when the hook of Guard needs a POSIX shell, so a run with a guard does not start on Windows.
	GuardPosixOnly bool
	// NoSkillCalls is true when the CLI reports no skill calls on its output, so a step that requires skills cannot be proved and does not start.
	NoSkillCalls bool
	// DefaultTimeout is the total limit of a run whose spec sets no Timeout. Zero means no limit. StallNote is added to the Note of a run that a total or idle
	// limit ended, for a CLI that prints nothing and retries when it cannot work (for example a model over its quota). It names the likely cause.
	DefaultTimeout time.Duration
	StallNote      string
}

// Stop names the cap that ended a run.
type Stop string

// The caps that end a run.
const (
	StopTurns  Stop = "turns"
	StopBudget Stop = "budget"
)

// Launch is what an adapter adds to the start of one run, beyond the arguments of its profile.
// Args are appended to the command line. Env is added to the environment after the allow-list; it never replaces a login variable or the token.
// Files maps a path inside the run folder to its content. The runner writes them before the CLI starts, with mode 0600 in a folder of mode 0700.
// A path must be local: no absolute path and no "..". The runner removes the folder when the run ends.
// Loaded is a local path in the run folder that the hook of the launch creates when it loads. The runner looks for it when the run ends, before it removes
// the folder, and sets Result.GuardMissing when it is not there. Empty means the launch has no proof of the load.
type Launch struct {
	Args   []string
	Env    map[string]string
	Files  map[string]string
	Loaded string
}

// Empty is true when the launch adds nothing to the run.
func (l Launch) Empty() bool {
	return len(l.Args) == 0 && len(l.Env) == 0 && len(l.Files) == 0 && l.Loaded == ""
}

// Tokens counts the tokens of a run. Input holds every input token, the cached ones included; Output holds every output token, the reasoning ones included.
// CachedInput and Reasoning say how much of Input and Output they are.
type Tokens struct {
	Input       int64
	CachedInput int64
	Output      int64
	Reasoning   int64
}

// Add returns the sum of two counts.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.CachedInput + o.CachedInput, t.Output + o.Output, t.Reasoning + o.Reasoning}
}

// Call is one shell call: the id that the CLI gave the call, and its command. The id is empty when the line has none.
type Call struct {
	ID   string
	Text string
}

// Seen is what one output line holds.
// Refused lists the ids of calls that the CLI refused before they ran, so those calls never ran.
// Result is true for the line that ends a run. Invalid is true for a line that the adapter cannot read; an empty line is not invalid.
// Stop is the cap that the CLI reports on its result line: StopTurns, StopBudget, or empty when the line reports no cap.
// Failed is the error subtype of a result line that reports an error and no cap, or empty when the line reports none.
// Cost is the total cost in dollars that the CLI reports on its result line, or 0 when the line reports none.
// Spent is the dollars that the line reports for the part of the run that it closes, or 0 when it reports none; the runner adds the Spent of all lines
// (Result.Streamed), and it is the only reader of Spent. A value that is not above 0 adds nothing. A line past the length limit of the runner is dropped
// before any reader sees it, so its dollars are lost.
// Priced is true when the line carries a cost field, even when the cost is 0, so a free run (a local model) differs from a CLI that reports no cost at all.
// A line with Spent above 0 counts as priced without it. A capped run of a profile with StreamsCost ends as a budget cap only when no line was priced.
// A CLI that reports a run total in Cost does not also set Spent for the same dollars.
// Usage is the tokens that the line reports for the part of the run that it closes, or zero when it reports none; the caller adds the Usage of all lines.
type Seen struct {
	Cost    float64
	Spent   float64
	Usage   Tokens
	Stop    Stop
	Failed  string
	Shell   []Call
	Skills  []string
	Refused []string
	Result  bool
	Invalid bool
	Priced  bool
}

// Adapter is one agent CLI. The runner calls Profile and, when the spec sets a guard, Guard before it starts a run.
// Call order for one run: Profile, then Guard or Sandbox, before the CLI starts, then Line and Calls for each output line while it runs, then TranscriptPath after it ends.
// Line and Calls never see a line of an earlier run, and TranscriptPath needs the session id that Line returned.
type Adapter interface {
	// Profile returns the command line and the login variables of the CLI.
	Profile() Profile
	// Guard returns the launch that makes the CLI run command before each shell command of one run and refuse the shell command when command exits with code 2.
	// It returns an empty launch when the CLI has no such option. runDir is the folder of the run: a path in Files is relative to it, and Args or Env can name a file in it.
	Guard(command, runDir string) Launch
	// Sandbox returns the launch that confines the shell of one run to the plan, and nothing else the CLI reads can widen it: no settings file and no MCP server.
	// A non-empty guard is the hook of Guard, and the launch carries it in the same settings as the sandbox. Only a profile with Sandbox true is asked.
	Sandbox(plan SandboxPlan, guard, runDir string) Launch
	// Line reads one line of the output of the CLI. It returns the session id and the message text that the line holds, or empty strings.
	Line(line string) (session, text string)
	// Calls reads one line of the output of the CLI and returns what it holds. More than one reader asks for the same line (the runner for the turn cap and
	// the dollars, the caller for the calls), so a call that keeps state for a line must not change what a later reader gets for it in any field but
	// Seen.Spent. The runner reads Seen.Spent once for each line; no other reader does, so a dedupe of the dollars by message id is allowed.
	Calls(line string) Seen
	// TranscriptPath returns where the CLI keeps the transcript of a session that ran in dir with the login folder configDir. Nothing reads the file.
	TranscriptPath(configDir, dir, session string) string
}
