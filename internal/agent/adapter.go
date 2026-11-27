// Package agent starts coding-agent CLIs. An Adapter supplies the profile of one CLI and reads its output stream.
package agent

import (
	"context"
	"strings"
	"time"

	"github.com/tobocop2/beehest/internal/model"
)

// VersionArgs are arguments that a CLI takes from the version From on, such as "2.0.0".
type VersionArgs struct {
	From string
	Args []string
}

// Profile is the command line and the login variables of one agent CLI. The launch says how the prompt reaches it (Launch.Prompt).
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
	// ConfigHome is the folder under the home folder where the CLI keeps its config when the runner sets no config folder. Empty means the CLI has none.
	ConfigHome string
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
	// SupportedFrom and SupportedBefore bound the versions of the CLI that the adapter has a recording of: a run on an older version, on SupportedBefore or on a
	// later one does not start, whatever the spec sets, and neither does a run whose version cannot be read. Both are full versions such as "1.19.5", or both are
	// empty and the profile sets no range.
	// A CLI older than SupportedFrom can fail to load the hook of Guard, and then it runs every shell command with no check and exits with code 0.
	SupportedFrom   string
	SupportedBefore string
	// Windows are the usage windows that ReadUsage reports, from the shortest to the longest, each with the default cap of a pool. A reading holds these windows only, in this order.
	// A profile of a CLI that cannot report usage has none. Two agents that declare one name declare one default.
	Windows []model.WindowSpec
	// VersionArgs are arguments that only a CLI of the version From or later takes. The runner adds each entry whose From is at or below the version that the
	// CLI prints, after Args and before the model. A profile with VersionArgs sets the supported range, so the version is always read.
	VersionArgs []VersionArgs
	// GuardBefore is the first version of the CLI whose hook of Guard is known not to refuse a command. A step with a guard does not start on that version or a
	// later one. It needs the supported versions, so the runner reads the version. Empty means no such version is known.
	GuardBefore string
	// GuardPosixOnly is true when the hook of Guard needs a POSIX shell, so a run with a guard does not start on Windows.
	GuardPosixOnly bool
	// SkillLine returns the line that makes the CLI load the skill of the given name before the first turn of the model, when it is the first line of the prompt.
	// A nil SkillLine means the CLI has no such line, so a step with the skill keyword does not start on it.
	SkillLine func(name string) string
	// DefaultTimeout is the total limit of a run whose spec sets no Timeout. Zero means no limit. StallNote is added to the Note of a run that a total or idle
	// limit ended, for a CLI that prints nothing and retries when it cannot work (for example a model over its quota). It names the likely cause.
	DefaultTimeout time.Duration
	StallNote      string
}

// NoUsage returns the reading of an agent that cannot report usage. The pool never picks a runner with such a reading.
func NoUsage(name model.Agent) model.Reading {
	return model.Unknown("the " + string(name) + " agent cannot report usage")
}

// Stop names the cap that ended a run.
type Stop string

// The caps that end a run.
const (
	StopTurns  Stop = "turns"
	StopBudget Stop = "budget"
	// StopContext is the stop of a run that the runner ended at the context limit of the step. The CLI never reports it.
	StopContext Stop = "context"
)

// LimitKind names the provider limit that refused more work.
type LimitKind string

// The provider limits that stop a run.
const (
	// LimitRate is a usage window of the plan, such as five hours or seven days.
	LimitRate LimitKind = "rate"
	// LimitSpend is a cap on the dollars that the account spends past its plan.
	LimitSpend LimitKind = "spend"
	// LimitOverload is a provider that is too busy to serve the request. It has no reset time, and a short wait cures it.
	LimitOverload LimitKind = "overload"
)

// Limit is a provider limit that refused more work. Window is the name that the CLI gives the limit, such as five_hour, or empty when it names none.
// ResetsAt is the time when the provider takes work again, or the zero time when the line gives none.
// The JSON form is the form that the engine records.
type Limit struct {
	Kind     LimitKind `json:"kind"`
	Window   string    `json:"window,omitempty"`
	ResetsAt time.Time `json:"resets_at,omitzero"`
}

// Name names the limit for a message, such as "rate limit five_hour", "rate limit" when the CLI names no window, or "overload".
func (l Limit) Name() string {
	if l.Kind == LimitOverload {
		return string(l.Kind)
	}

	return strings.TrimSpace(string(l.Kind) + " limit " + l.Window)
}

// PromptMode is the way that the prompt reaches the CLI.
type PromptMode int

// The ways that the prompt reaches the CLI. The zero value is PromptStdin.
const (
	// PromptStdin sends the prompt on standard input.
	PromptStdin PromptMode = iota
	// PromptArgument puts the prompt in one argument, the last one. The prompt shows in the process list of the machine, and the argument limit of the system bounds it.
	PromptArgument
	// PromptFile writes the prompt to a file in the run folder and puts the path of the file in the last argument.
	PromptFile
)

// Launch is what an adapter adds to the start of one run, beyond the arguments of its profile.
// Prompt says how the prompt reaches the CLI. For PromptArgument and PromptFile the runner appends one argument after every other argument, so a flag that
// names the prompt goes last in Args. The file of PromptFile is beehest-prompt.txt in the run folder, has mode 0600 and is removed with the folder when the run ends. A launch file of that name is replaced.
// Prefix is a list of words that the runner puts before the CLI on the command line, such as a wrapper that sets up an environment. The first word is looked
// up on PATH, and the runner does not look up the CLI itself: the program of the prefix finds it by name. Each word is one argument and no shell reads the line.
// The probe of the version of the CLI runs the prefix, then the CLI, then --version.
// Args are appended to the command line. Env is added to the environment after the allow-list; it never replaces a login variable or the token.
// Files maps a path inside the run folder to its content. The runner writes them before the CLI starts, with mode 0600 in a folder of mode 0700.
// A path must be local: no absolute path and no "..". The runner removes the folder when the run ends.
// Copies maps a local path inside the run folder to a folder that the runner copies there, with its files, before the CLI starts.
// Loaded is a local path in the run folder that the hook of the launch creates when it loads. The runner looks for it when the run ends, before it removes
// the folder, and sets Result.GuardMissing when it is not there. Empty means the launch has no proof of the load.
type Launch struct {
	Args   []string
	Env    map[string]string
	Files  map[string]string
	Copies map[string]string
	Loaded string
	Prompt PromptMode
	Prefix []string
	// Hooked is true when the launch holds the hook that refuses a command before it runs. An adapter sets it only when it adds that hook for a guard command.
	// A launch can hold other settings with no hook, so the arguments do not tell.
	Hooked bool
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

// Denial is one tool call that the permission check of the CLI refused, by a rule or by the permission mode: the name of the tool and its target, the file path of a file tool or the
// command of a shell call. Edit is true for a call of a file tool that writes its target. Closed is true for an Edit whose target a deny rule of the
// sandbox covers, so the refusal is that rule at work.
type Denial struct {
	Tool   string `json:"tool"`
	Target string `json:"target"`
	Edit   bool   `json:"edit,omitempty"`
	Closed bool   `json:"closed,omitempty"`
}

// Seen is what one output line holds.
// Denials lists the tool calls that the CLI reports on its result line as refused by its permission check, or nil when the line reports none.
// Refused lists the ids of calls that the CLI refused before they ran, so those calls never ran.
// Skills lists the skill calls of the line with their ids. Succeeded lists the ids of the calls whose result the line reports with no error; a skill
// counts as loaded only when its call id is in the Succeeded of a later line.
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
// Limit is the provider limit that the line reports as the reason the CLI refused more work, or nil when it reports none. A line that only warns that
// a limit is near reports none.
// Denied is the reason why the provider refused more work in a way that no wait cures, such as usage credits that the account does not have or a pause of
// the organization, or empty when the line reports none. A line with Denied reports no Limit.
// Context is the tokens in the context window after the turn that the line closes: the input tokens plus the cache read and cache creation tokens of that turn.
// It is 0 when the line reports none, and for a CLI that cannot report it; the context limit of a step then never fires. A line of a sub-agent reports none.
// Said is the message texts and the tool calls of an assistant line, for the stream that a reader follows. A tool call reads "<tool>: <target>".
type Seen struct {
	Cost      float64
	Spent     float64
	Usage     Tokens
	Context   int64
	Stop      Stop
	Failed    string
	Shell     []Call
	Skills    []Call
	Succeeded []string
	Refused   []string
	Denials   []Denial
	Result    bool
	Invalid   bool
	Priced    bool
	Limit     *Limit
	Denied    string
	Said      []Said
}

// Said is one thing the agent said on an output line: the text of a message, or a tool call with its target. A line lists them in the order of the message.
type Said struct {
	Call bool
	Text string
}

// Adapter is one agent CLI. The runner calls Profile and, when the spec sets a guard, Guard before it starts a run.
// Call order for one run: Profile, then Guard or Sandbox, before the CLI starts, then Line and Calls for each output line while it runs, then TranscriptPath after it ends.
// Line and Calls never see a line of an earlier run, and TranscriptPath needs the session id that Line returned. A start that follows a lost run also
// calls TranscriptPath before the CLI starts, with the session that Line returned in the lost run.
// ReadUsage runs outside a run, when the pool or the doctor asks for the usage of one runner of this agent.
type Adapter interface {
	// Profile returns the command line and the login variables of the CLI.
	Profile() Profile
	// ReadUsage returns the usage of one runner of this agent, or a reading that says the agent cannot report it.
	ReadUsage(ctx context.Context, runner model.Runner) model.Reading
	// Guard returns the launch of a run with no sandbox. A non-empty command makes the CLI run command before each shell command and refuse the shell command
	// when command exits with code 2; the launch is empty of a hook when the CLI has no such option. An empty command adds no hook.
	// write lists the files that the file tools of the run may read and write, each by its exact path (Spec.Write): the launch opens each one, whatever the command.
	// skills lists the folder of each skill that the run loads (Spec.Skills): the launch copies each one into the run folder and loads it as a plugin, if the CLI has such a plugin.
	// The launch adds nothing for command, write and skills when they are empty. It can still carry the settings that keep the commits of the run free of an attribution trailer.
	// runDir is the folder of the run: a path in Files is relative to it, and Args or Env can name a file in it.
	Guard(command, runDir string, write, skills []string) Launch
	// Sandbox returns the launch that confines the shell of one run to the plan, and nothing else the CLI reads can widen it: no settings file and no MCP server.
	// A non-empty guard is the hook of Guard, and the launch carries it in the same settings as the sandbox. Only a profile with Sandbox true is asked.
	// write and skills are the same lists as for Guard, and the shell may write the files of write too.
	Sandbox(plan SandboxPlan, guard, runDir string, write, skills []string) Launch
	// Line reads one line of the output of the CLI. It returns the session id and the message text that the line holds, or empty strings.
	Line(line string) (session, text string)
	// Calls reads one line of the output of the CLI and returns what it holds. More than one reader asks for the same line (the runner for the turn cap and
	// the dollars, the caller for the calls), so a call that keeps state for a line must not change what a later reader gets for it in any field but
	// Seen.Spent. The runner reads Seen.Spent once for each line; no other reader does, so a dedupe of the dollars by message id is allowed.
	Calls(line string) Seen
	// TranscriptPath returns where the CLI keeps the transcript of a session that ran in dir with the login folder configDir. Before a start that follows
	// a lost run, the agents resume the session of that run only when the file exists, and they do not read it. A CLI that keeps no file for one session
	// (it keeps every session in one database) returns a distinct path inside configDir that no file holds, and a lost run of it never resumes.
	TranscriptPath(configDir, dir, session string) string
}
