// Package agent starts coding-agent CLIs. An Adapter supplies the profile of one CLI and reads its output stream.
package agent

// Profile is the command line and the login variables of one agent CLI. The prompt always goes on stdin.
// ModelFlag and ResumeFlag are the flags that take a model name and a session id. An empty flag means the CLI has no such option.
type Profile struct {
	Binary string
	Args   []string
	// EditArgs follow Args unless the spec sets NoEdits. They let the CLI edit files in the work folder.
	EditArgs   []string
	ConfigVars []string
	TokenVar   string
	ModelFlag  string
	ResumeFlag string
	// TurnsFlag and BudgetFlag are the flags that cap the turns and the dollars of one run. An empty flag means the CLI has no such option.
	TurnsFlag  string
	BudgetFlag string
}

// Stop names the cap that ended a run.
type Stop string

// The caps that end a run.
const (
	StopTurns  Stop = "turns"
	StopBudget Stop = "budget"
)

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
type Seen struct {
	Stop    Stop
	Failed  string
	Shell   []Call
	Skills  []string
	Refused []string
	Result  bool
	Invalid bool
}

// Adapter is one agent CLI. The runner calls Profile and, when the spec sets a guard, Guard before it starts a run.
// Call order for one run: Profile and Guard before the CLI starts, then Line and Calls for each output line while it runs, then TranscriptPath after it ends.
// Line and Calls never see a line of an earlier run, and TranscriptPath needs the session id that Line returned.
type Adapter interface {
	// Profile returns the command line and the login variables of the CLI.
	Profile() Profile
	// Guard returns the arguments that make the CLI run command before each shell command of one run and refuse the shell command when command exits with code 2.
	// It returns nil when the CLI has no such option.
	Guard(command string) []string
	// Line reads one line of the output of the CLI. It returns the session id and the message text that the line holds, or empty strings.
	Line(line string) (session, text string)
	// Calls reads one line of the output of the CLI and returns what it holds.
	Calls(line string) Seen
	// TranscriptPath returns where the CLI keeps the transcript of a session that ran in dir with the login folder configDir. Nothing reads the file.
	TranscriptPath(configDir, dir, session string) string
}
