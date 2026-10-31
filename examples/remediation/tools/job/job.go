// Package job judges a job script for the host: exit-code capture, timeouts on tests and the rules of the gate list. It reads the script with the parser of check/shell.
package job

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

var makeTarget = regexp.MustCompile(`^test[A-Za-z0-9_.:-]*$`)

// isQuoted reports whether the word has a quoted part.
func isQuoted(word *syntax.Word) bool {
	return slices.ContainsFunc(word.Parts, func(part syntax.WordPart) bool {
		switch part.(type) {
		case *syntax.DblQuoted, *syntax.SglQuoted:
			return true
		}

		return false
	})
}

// hasExpansion reports whether the word holds a parameter, command or arithmetic expansion that the shell runs.
func hasExpansion(word *syntax.Word) bool {
	found := false

	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp:
			found = true
		}

		return !found
	})

	return found
}

// Script returns the FAIL lines for a job script: a line that does not parse, a command no static read can see, bulk staging, and a test command with no timeout in front of it.
func Script(script string) []string {
	var lines []string

	for _, finding := range shell.Scan(script) {
		if line := jobLine(finding); line != "" {
			lines = append(lines, line)
		}
	}

	return append(lines, newJob(script).untimedLines()...)
}

func jobLine(finding model.Finding) string {
	switch finding.Kind {
	case model.FindingBulkStaging:
		return "FAIL: the job stages in bulk (" + finding.Text + "). Stage explicit paths only, or set GIT_INDEX_FILE for a private index"
	case model.FindingUnparsed:
		return unparsedMessage(finding.Text)
	case model.FindingOpaque:
		return "FAIL: the job runs a command the check cannot read (" + finding.Text + "). Write the command name out, with no variable or wrapper that hides it"
	case model.FindingRemoteDelete, model.FindingDeleteText, model.FindingLocalDelete,
		model.FindingGitAuthor, model.FindingGitBypass, model.FindingEnvOverride, model.FindingConfigRead, model.FindingGitWrite, model.FindingProtected:
	}

	return ""
}

func unparsedMessage(line string) string {
	return "FAIL: the job has a line that does not parse as shell: " + line + ". Fix the syntax"
}

// site is one simple command of the script.
type site struct {
	u    shell.Unit
	stmt *syntax.Stmt
	call *syntax.CallExpr
}

// job is a parsed job script: its commands, and which functions run their arguments.
type job struct {
	sites []site
	wraps map[string]bool
	words *shell.Reader
}

func newJob(script string) *job {
	j := &job{wraps: map[string]bool{}, words: shell.NewReader()}

	units, _ := shell.ParseUnits(script) // Script reports the lines that do not parse
	for _, u := range units {
		j.collect(u)
	}

	return j
}

func (j *job) collect(u shell.Unit) {
	syntax.Walk(u.File, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.FuncDecl:
			if typed.Name != nil {
				j.wraps[typed.Name.Value] = j.runsItsArguments(u, typed.Body)
			}
		case *syntax.Stmt:
			if call, ok := typed.Cmd.(*syntax.CallExpr); ok {
				j.sites = append(j.sites, site{u, typed, call})
			}
		}

		return true
	})
}

// runsItsArguments reports whether the body of a function runs its arguments (`"$@"`, `$1`, `$*` or eval of them).
func (j *job) runsItsArguments(u shell.Unit, body *syntax.Stmt) bool {
	found := false

	syntax.Walk(body, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && !found {
			found = j.callRunsArguments(u, call)
		}

		return !found
	})

	return found
}

func (j *job) callRunsArguments(u shell.Unit, call *syntax.CallExpr) bool {
	head, _ := shell.HeadOf(j.words.Words(u, call.Args))
	if len(head) == 0 {
		return false
	}

	args := call.Args[len(call.Args)-len(head):]
	if name, ok := shell.Lone(args[0]); ok && shell.OneOf(name, "@", "*", "1") {
		return true
	}

	evaluates := head[0] == shell.CmdEval || (shell.IsShell(head[0]) && len(head) > 1 && head[1] == "-c")

	return evaluates && slices.ContainsFunc(args[1:], hasExpansion)
}

// invocation is a command with its assignments and wrappers dropped.
type invocation struct {
	head   []string
	args   []*syntax.Word
	timed  bool
	lookup bool
}

// invocation drops the wrappers of a call, and a module word (`runner -m tool`), until the command is left.
func (j *job) invocation(u shell.Unit, call *syntax.CallExpr) invocation {
	words, args := j.words.Words(u, call.Args), call.Args
	inv := invocation{}

	for {
		head, prefix := shell.HeadOf(words)
		inv.timed = inv.timed || dropped(prefix, shell.TimeoutName) || dropped(prefix, shell.GtimeoutName)
		inv.lookup = inv.lookup || (dropped(prefix, "command") && (slices.Contains(prefix, "-v") || slices.Contains(prefix, "-V")))
		words, args = head, args[len(args)-len(head):]

		if !runsModule(words) {
			break
		}

		words, args = words[2:], args[2:]
	}

	inv.head, inv.args = words, args

	return inv
}

// runsModule reports whether the words are a runner and `-m` before a module word. A test tool's own `-m` option and a carrier's option are not.
func runsModule(words []string) bool {
	return len(words) >= 2 && words[1] == "-m" && !startsTool(words) && !carries(words)
}

func dropped(prefix []string, name string) bool {
	return slices.ContainsFunc(prefix, func(word string) bool { return path.Base(word) == name })
}

// startsTool reports whether the words, which are not empty, start with a test command.
func startsTool(words []string) bool {
	switch path.Base(words[0]) {
	case "pytest", "py.test", "bats":
		return true
	case "cargo":
		rest := words[1:]
		if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
			rest = rest[1:]
		}

		return len(rest) > 0 && shell.OneOf(rest[0], "test", "nextest")
	case "make":
		return len(words) > 1 && makeTarget.MatchString(words[1])
	}

	return false
}

// toolIn returns the command text from the first test command in the words, unless a timeout comes before it.
func (j *job) toolIn(words []string) string {
	for at, word := range words {
		if shell.IsTimeout(path.Base(word)) {
			return ""
		}

		if startsTool(words[at:]) {
			return strings.Join(words[at:], " ")
		}
	}

	return ""
}

// untimed returns the command text when the call runs a test command with no timeout in front of it.
func (j *job) untimed(u shell.Unit, call *syntax.CallExpr) string {
	if found := j.quotedAssign(u, call); found != "" {
		return found
	}

	inv := j.invocation(u, call)
	if inv.timed || inv.lookup || len(inv.head) == 0 {
		return ""
	}

	return j.untimedCommand(inv)
}

func (j *job) untimedCommand(inv invocation) string {
	name := path.Base(inv.head[0])
	runsArgs, isFunc := j.wraps[name]

	switch {
	case startsTool(inv.head):
		return strings.Join(inv.head, " ")
	case carries(inv.head) || runsArgs:
		return j.codeIn(inv.head[1:], inv.args[1:], true)
	case isFunc || strings.HasPrefix(name, "$"):
		return j.plainTail(inv.head[1:], inv.args[1:])
	}

	return ""
}

// quotedAssign checks the quoted value of an assignment: it may hold a command line.
func (j *job) quotedAssign(u shell.Unit, call *syntax.CallExpr) string {
	for _, assign := range call.Assigns {
		if assign.Value != nil && isQuoted(assign.Value) {
			if found := j.toolIn(strings.Fields(j.words.Literal(u, assign.Value))); found != "" {
				return found
			}
		}
	}

	return ""
}

// plainTail checks the arguments of a function or variable that does not run them: a quoted string is text, unless a carrier word shows the arguments run.
func (j *job) plainTail(words []string, args []*syntax.Word) string {
	if found := j.codeIn(words, args, false); found != "" {
		return found
	}

	for k, word := range words {
		if !isQuoted(args[k]) && carrierWord(path.Base(word)) {
			return j.codeIn(words, args, true)
		}
	}

	return ""
}

// codeIn checks the words after a command that runs them. A quoted string is shell text when stringsRun is true, and is skipped when it is false.
func (j *job) codeIn(words []string, args []*syntax.Word, stringsRun bool) string {
	var flat []string

	for k, word := range words {
		switch {
		case !stringsRun && isQuoted(args[k]):
		case stringsRun && strings.ContainsAny(word, " \t\n"):
			found, unread := j.nested(word)
			if found != "" {
				return found
			}

			flat = append(flat, unread...)
		default:
			flat = append(flat, word)
		}
	}

	return j.toolIn(flat)
}

// nested checks a string that a carrier runs as shell. It returns the words of the lines that do not parse.
func (j *job) nested(text string) (string, []string) {
	var unread []string

	units, bad := shell.ParseUnits(text)
	for _, line := range bad {
		unread = append(unread, strings.Fields(line)...)
	}

	for _, u := range units {
		for _, call := range unitCalls(u) {
			if found := j.untimed(u, call); found != "" {
				return found, nil
			}
		}
	}

	return "", unread
}

// carries reports whether the command runs the command line in its arguments.
func carries(head []string) bool {
	name := path.Base(head[0])

	return isCarrier(name) || (shell.OneOf(name, "uv", "poetry", "pipx") && uvRuns(head[1:]))
}

func isCarrier(name string) bool {
	switch name {
	case "bash", "sh", "zsh", "dash", shell.SSHName, shell.CmdEval, "docker", "podman", "flock", "parallel", "watch", "su", "uvx":
		return true
	}

	return false
}

func carrierWord(name string) bool {
	return isCarrier(name) || shell.OneOf(name, "uv", "poetry", "pipx")
}

// uvRuns reports whether the words after `uv` are options, an optional `tool`, and then `run`. An option may take the next word as its value.
// reach marks each index the walk can stand on, so the work is linear in the number of words.
func uvRuns(words []string) bool {
	reach := make([]bool, len(words)+shell.ValueOptionWidth)
	reach[0] = true

	for at, word := range words {
		if !reach[at] {
			continue
		}

		if isRun(words[at:]) {
			return true
		}

		if strings.HasPrefix(word, "-") {
			reach[at+1] = true
			reach[at+shell.ValueOptionWidth] = at+1 < len(words) && !strings.HasPrefix(words[at+1], "-")
		}
	}

	return false
}

func isRun(words []string) bool {
	if words[0] == "tool" {
		words = words[1:]
	}

	return len(words) > 0 && words[0] == "run"
}

func (j *job) untimedLines() []string {
	var lines []string

	ran := j.ranFiles()

	for _, s := range j.sites {
		if found := j.untimed(s.u, s.call); found != "" {
			lines = append(lines, bodyMessage(found, ""))
		}

		lines = append(lines, j.heredocLines(s, ran)...)
	}

	return lines
}

// heredocLines checks the heredoc bodies of a command that are code: a body fed to a shell, or a body written to a file the job runs.
func (j *job) heredocLines(s site, ran map[string]bool) []string {
	var lines []string

	file := j.heredocFile(s)

	for _, redir := range s.stmt.Redirs {
		if (redir.Op != syntax.Hdoc && redir.Op != syntax.DashHdoc) || redir.Hdoc == nil {
			continue
		}

		switch {
		case feedsShell(j.words.Words(s.u, s.call.Args)):
			lines = append(lines, j.bodyLines(shell.SrcOf(s.u.Src, redir.Hdoc), "")...)
		case file != "" && ran[file]:
			lines = append(lines, j.bodyLines(shell.SrcOf(s.u.Src, redir.Hdoc), file)...)
		}
	}

	return lines
}

func (j *job) bodyLines(body, file string) []string {
	var lines []string

	units, bad := shell.ParseUnits(body)
	for _, line := range bad {
		lines = append(lines, unparsedMessage(line))
	}

	for _, u := range units {
		for _, found := range unitCalls(u) {
			if text := j.untimed(u, found); text != "" {
				lines = append(lines, bodyMessage(text, file))
			}
		}
	}

	return lines
}

func bodyMessage(found, file string) string {
	if file == "" {
		return "FAIL: the job runs a test command without timeout in front of it: " + found + ". Write timeout <seconds> " + found + ". If the line only names a test tool in a message, quote the message"
	}

	return "FAIL: the job runs a test command without timeout in front of it: " + found + " (written to " + file + " by a heredoc and run later). Write timeout <seconds> " + found
}

func unitCalls(u shell.Unit) []*syntax.CallExpr {
	var calls []*syntax.CallExpr

	syntax.Walk(u.File, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok {
			calls = append(calls, call)
		}

		return true
	})

	return calls
}

func feedsShell(words []string) bool {
	return slices.ContainsFunc(words, func(word string) bool {
		name := path.Base(word)

		return shell.IsShell(name) || name == shell.SSHName
	})
}

// heredocFile returns the file a command writes its heredoc to (`> f`, `>> f` or `tee f`), or an empty string.
func (j *job) heredocFile(s site) string {
	for _, redir := range s.stmt.Redirs {
		if (redir.Op == syntax.RdrOut || redir.Op == syntax.AppOut) && redir.Word != nil {
			return j.words.Literal(s.u, redir.Word)
		}
	}

	head, _ := shell.HeadOf(j.words.Words(s.u, s.call.Args))
	if len(head) == 0 || path.Base(head[0]) != "tee" {
		return ""
	}

	for _, word := range head[1:] {
		if !strings.HasPrefix(word, "-") {
			return word
		}
	}

	return ""
}

// ranFiles returns the files that a heredoc writes and that a command of the job later runs.
func (j *job) ranFiles() map[string]bool {
	written := j.writtenFiles()
	if len(written) == 0 {
		return written
	}

	targets := map[string]bool{}

	for _, s := range j.sites {
		targets[j.runTarget(s)] = true
	}

	for file := range written {
		written[file] = targets[strings.TrimPrefix(file, "./")]
	}

	return written
}

// writtenFiles returns the files that a heredoc of the job writes.
func (j *job) writtenFiles() map[string]bool {
	written := map[string]bool{}

	for _, s := range j.sites {
		if file := j.heredocFile(s); file != "" && hasHeredoc(s.stmt) {
			written[file] = true
		}
	}

	return written
}

func hasHeredoc(stmt *syntax.Stmt) bool {
	return slices.ContainsFunc(stmt.Redirs, func(redir *syntax.Redirect) bool {
		return redir.Op == syntax.Hdoc || redir.Op == syntax.DashHdoc
	})
}

// runTarget returns the file a command runs: as the command, or as the argument of a shell or `source`.
func (j *job) runTarget(s site) string {
	head, _ := shell.HeadOf(j.words.Words(s.u, s.call.Args))
	if len(head) > 1 && (head[0] == "source" || head[0] == ".") {
		head = head[1:]
	}

	if len(head) == 0 {
		return ""
	}

	return strings.TrimPrefix(head[0], "./")
}
