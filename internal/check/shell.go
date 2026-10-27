// Package check judges the shell commands of a lane and the scripts of a job against the round rules.
package check

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/obee/internal/model"
)

const (
	// maxResolvedText is the size past which the value of a variable is unreadable: the variable stays `$name` text.
	maxResolvedText = 4096
	// maxResolutions is the number of values one lookup may work out before it gives up.
	maxResolutions = 1000
)

var (
	aliasName = regexp.MustCompile(`^[A-Za-z_][\w-]*$`)
	varRef    = regexp.MustCompile(`\$(?:\{(\w+)\}|(\w+))`)
)

// resolution is the value of a variable with every variable in it replaced by its own value.
type resolution struct {
	text string
	held bool
}

// command is one simple command: its words, the command left after the wrappers, the dropped words, its heredoc bodies and the command piped into it.
type command struct {
	head, prefix []string
	heredocs     []string
	expanded     []string
	unread       string // the variable that is the command word when its value is past a cap
	prev         *command
}

func newScanner() *scanner {
	return &scanner{
		vars: map[string]string{}, funcs: map[string]bool{}, active: map[string]bool{},
		memo: map[string]resolution{}, local: map[string]resolution{}, resolving: map[string]bool{},
	}
}

// scanner holds what one Scan knows: the wrapper variables, the wrapper functions and the findings.
type scanner struct {
	vars        map[string]string
	funcs       map[string]bool
	active      map[string]bool       // variables whose value is being read: they stay `$name` text inside it
	memo        map[string]resolution // values that depend on nothing that changes: kept for the whole scan
	local       map[string]resolution // values that name a variable being worked out: kept for the current lookup only
	resolving   map[string]bool       // variables whose value is being worked out now
	budget      int                   // values the current lookup may still work out
	cut         bool                  // the current lookup ran out of budget
	resolutions int                   // values worked out so far, for the tests
	used        []string              // variables the current command expanded
	findings    []model.Finding
}

// value returns the wrapper value of a variable, unless the scan is already reading that value or the value is unreadable.
func (s *scanner) value(name string) (string, bool) {
	if s.active[name] {
		return "", false
	}

	s.budget, s.cut = maxResolutions, false
	clear(s.local)

	text, held, _ := s.resolve(name)
	if s.cut || !held {
		return "", false
	}

	s.used = append(s.used, name)

	return text, true
}

// resolve returns the value of a variable with the variables inside it replaced, each worked out once.
// A variable that is unset, being worked out, too long or out of budget stays `$name` text. open is true when the result depends on that state, so it is kept for the current lookup only.
func (s *scanner) resolve(name string) (string, bool, bool) {
	if got, ok := s.memo[name]; ok {
		return got.text, got.held, false
	}

	if got, ok := s.local[name]; ok {
		return got.text, got.held, true
	}

	raw, ok := s.vars[name]
	if !ok {
		return "", false, false
	}

	return s.work(name, raw)
}

// work reads the value of a variable once, unless that variable is being worked out already or the lookup is out of budget.
func (s *scanner) work(name, raw string) (string, bool, bool) {
	if s.resolving[name] || s.budget == 0 {
		s.cut = s.cut || s.budget == 0

		return "", false, true
	}

	s.budget--
	s.resolutions++
	s.resolving[name] = true

	text, open := s.substitute(raw)
	delete(s.resolving, name)

	return s.keep(name, text, open)
}

// keep stores the value of a variable, for the scan or for the current lookup if it depends on state that changes, and drops a value past the size cap.
func (s *scanner) keep(name, text string, open bool) (string, bool, bool) {
	held := len(text) <= maxResolvedText
	if !held {
		text = ""
	}

	if open {
		s.local[name] = resolution{text, held}
	} else {
		s.memo[name] = resolution{text, held}
	}

	return text, held, open
}

// substitute replaces each readable variable in the text by its value. open is true when one of them was not kept.
func (s *scanner) substitute(raw string) (string, bool) {
	open := false
	text := varRef.ReplaceAllStringFunc(raw, func(ref string) string {
		match := varRef.FindStringSubmatch(ref)
		inner, held, innerOpen := s.resolve(match[1] + match[2])
		open = open || innerOpen

		if !held {
			return ref
		}

		return inner
	})

	return text, open
}

// hold marks the variables as being read and returns the function that unmarks the ones it marked.
func (s *scanner) hold(names []string) func() {
	var marked []string

	for _, name := range names {
		if !s.active[name] {
			s.active[name] = true
			marked = append(marked, name)
		}
	}

	return func() {
		for _, name := range marked {
			delete(s.active, name)
		}
	}
}

// Scan returns the forbidden commands in one shell command, each once, in the order found.
func Scan(commandText string) []model.Finding {
	s := newScanner()
	s.prepass(commandText)
	s.scan(commandText, false)

	return dedupe(s.findings)
}

func dedupe(findings []model.Finding) []model.Finding {
	seen := map[model.Finding]bool{}

	var out []model.Finding

	for _, finding := range findings {
		if !seen[finding] {
			seen[finding] = true
			out = append(out, finding)
		}
	}

	return out
}

func (s *scanner) add(kind model.FindingKind, head []string) {
	s.findings = append(s.findings, model.Finding{Kind: kind, Text: strings.Join(head, " ")})
}

// walker collects the simple commands of one unit and links each to the command piped into it.
type walker struct {
	s      *scanner
	u      unit
	byStmt map[*syntax.Stmt]*command
	feeds  map[*syntax.Stmt]*syntax.Stmt
	out    []*command
}

// commands returns the simple commands of the text in source order, including those in command and process substitutions.
// A line of code that does not parse is a finding; text that is only printed or written (code false) may be prose.
func (s *scanner) commands(text string, code bool) []*command {
	var out []*command

	units, bad := parseUnits(text)
	for _, line := range bad {
		if code {
			s.findings = append(s.findings, model.Finding{Kind: model.FindingUnparsed, Text: line})
		}
	}

	for _, u := range units {
		w := &walker{s: s, u: u, byStmt: map[*syntax.Stmt]*command{}, feeds: map[*syntax.Stmt]*syntax.Stmt{}}
		syntax.Walk(u.file, w.visit)
		out = append(out, w.out...)
	}

	return out
}

func (w *walker) visit(node syntax.Node) bool {
	switch typed := node.(type) {
	case *syntax.BinaryCmd:
		if typed.Op == syntax.Pipe || typed.Op == syntax.PipeAll {
			w.feeds[typed.Y] = lastOfPipe(typed.X)
		}
	case *syntax.Stmt:
		if found := w.s.command(w.u, typed, w.byStmt[w.feeds[typed]]); found != nil {
			w.byStmt[typed] = found
			w.out = append(w.out, found)
		}
	}

	return true
}

func lastOfPipe(stmt *syntax.Stmt) *syntax.Stmt {
	if pipe, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && (pipe.Op == syntax.Pipe || pipe.Op == syntax.PipeAll) {
		return lastOfPipe(pipe.Y)
	}

	return stmt
}

func (s *scanner) command(u unit, stmt *syntax.Stmt, prev *command) *command {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil
	}

	s.used = nil
	words, names := s.namedWords(u, call.Args)
	head, prefix := headOf(words)
	found := &command{head: head, prev: prev}
	found.prefix = append(s.assigned(u, call), prefix...)
	found.expanded = s.used
	found.unread = s.unreadable(names, len(prefix), found.expanded)

	for _, redir := range stmt.Redirs {
		if (redir.Op == syntax.Hdoc || redir.Op == syntax.DashHdoc) && redir.Hdoc != nil {
			found.heredocs = append(found.heredocs, srcOf(u.src, redir.Hdoc))
		}
	}

	return found
}

// unreadable returns the name of the wrapper variable that is the command word when no value could be read for it: its value is past a cap.
// The command word is the word at index start, after the wrapper words. names holds, for each word, the name of the unread variable it is, or "".
// A variable the scan is already reading is not unreadable: it is a cycle, and the text it stays in is judged.
func (s *scanner) unreadable(names []string, start int, expanded []string) string {
	if start >= len(names) {
		return ""
	}

	name := names[start]
	if name == "" || s.active[name] || slices.Contains(expanded, name) {
		return ""
	}

	if _, recorded := s.vars[name]; !recorded {
		return ""
	}

	return name
}

// assigned returns the `name=value` words of the assignments before a command.
func (s *scanner) assigned(u unit, call *syntax.CallExpr) []string {
	var out []string

	for _, assign := range call.Assigns {
		if assign.Name != nil && assign.Value != nil {
			out = append(out, assign.Name.Value+"="+s.literal(u, assign.Value))
		}
	}

	return out
}

// scan adds the findings of the shell text. remote is true inside a remote command.
func (s *scanner) scan(text string, remote bool) {
	for _, cmd := range s.commands(text, true) {
		s.scanCommand(cmd, remote)
	}
}

// scanCommand judges one command. The text it reads again is read with the command's own variables held, so a variable that names itself ends the walk.
func (s *scanner) scanCommand(cmd *command, remote bool) {
	defer s.hold(cmd.expanded)()

	s.judge(cmd, remote)
}

func (s *scanner) judge(cmd *command, remote bool) {
	if staging := bulkHit(cmd.head); staging != "" && !hasPrivateIndex(cmd.prefix) {
		s.findings = append(s.findings, model.Finding{Kind: model.FindingBulkStaging, Text: staging})
	}

	s.scanOpaque(cmd)

	if len(cmd.head) == 0 {
		return
	}

	s.scanDeletes(cmd.head, remote)
	s.scanWords(cmd, remote)

	for _, body := range cmd.heredocs {
		s.contentDeletes(body)
	}

	if remoteText, ok := remoteOf(cmd.head, s.funcs); ok {
		s.scanRemote(cmd, remoteText)
	}
}

// scanWords reads the commands that carry a command line in their words: eval, echo, printf and a shell.
func (s *scanner) scanWords(cmd *command, remote bool) {
	name := path.Base(cmd.head[0])

	switch {
	case name == cmdEval && len(cmd.head) > 1:
		s.scan(strings.Join(cmd.head[1:], " "), remote)
	case name == "echo" || name == "printf":
		s.contentDeletes(strings.Join(cmd.head[1:], " "))
	case isShell(name):
		s.scanShell(cmd, remote)
	}
}

// scanOpaque adds a finding for a command whose real command no static read can see and a rule needs to.
func (s *scanner) scanOpaque(cmd *command) {
	if cmd.unread != "" {
		s.add(model.FindingOpaque, []string{"$" + cmd.unread})
	}

	switch {
	case len(cmd.head) > 0 && hidesRule(cmd.head):
		s.add(model.FindingOpaque, cmd.head[:1])
	case slices.Contains(cmd.prefix, cmdEnv) && splitsString(cmd.prefix):
		s.add(model.FindingOpaque, []string{cmdEnv, "-S"})
	case len(cmd.head) > 0 && findRunsShell(cmd.head):
		s.add(model.FindingOpaque, cmd.head)
	}
}

func (s *scanner) scanDeletes(head []string, remote bool) {
	if !deleteHit(head) {
		return
	}

	if remote {
		s.add(model.FindingRemoteDelete, head)

		return
	}

	if stopsLaptop(head) {
		s.add(model.FindingLocalDelete, head)
	}
}

// contentDeletes adds a delete-text finding for each command of text that deletes: text a command writes or prints.
func (s *scanner) contentDeletes(text string) {
	for _, cmd := range s.commands(strings.ReplaceAll(text, `\n`, newline), false) {
		if deleteHit(cmd.head) {
			s.add(model.FindingDeleteText, cmd.head)
		}
	}
}

// scanShell reads a shell given a string with -c, or a script on stdin.
func (s *scanner) scanShell(cmd *command, remote bool) {
	args := cmd.head[1:]

	if at := slices.IndexFunc(args, shellCommandOption.MatchString); at >= 0 && at+1 < len(args) {
		s.scan(args[at+1], remote)

		return
	}

	if !slices.ContainsFunc(args, func(word string) bool { return !strings.HasPrefix(word, "-") }) {
		for _, body := range cmd.heredocs {
			s.scan(body, remote)
		}
	}
}

// scanRemote reads a remote command, and the script its stdin carries.
func (s *scanner) scanRemote(cmd *command, remoteText string) {
	s.scan(remoteText, true)

	if !stdinIsScript(remoteText) {
		return
	}

	for _, body := range cmd.heredocs {
		s.scan(body, true)
	}

	if prev := cmd.prev; prev != nil && len(prev.head) > 0 && (path.Base(prev.head[0]) == "echo" || path.Base(prev.head[0]) == "printf") {
		s.scan(strings.ReplaceAll(strings.Join(prev.head[1:], " "), `\n`, newline), true)
	}
}

// prepass finds the variables, functions and aliases the text defines that hold a host wrapper.
func (s *scanner) prepass(text string) {
	var bodies [][2]string

	units, _ := parseUnits(text) // Scan reports the lines that do not parse

	for _, u := range units {
		syntax.Walk(u.file, func(node syntax.Node) bool {
			s.noteNode(u, node, &bodies)

			return true
		})
	}

	s.closeFuncs(bodies)
}

func (s *scanner) noteNode(u unit, node syntax.Node, bodies *[][2]string) {
	switch typed := node.(type) {
	case *syntax.CallExpr:
		s.noteCall(u, typed, bodies)
	case *syntax.DeclClause:
		for _, assign := range typed.Args {
			s.noteAssign(u, assign)
		}
	case *syntax.FuncDecl:
		if typed.Name != nil {
			*bodies = append(*bodies, [2]string{typed.Name.Value, srcOf(u.src, typed.Body)})
		}
	}
}

// closeFuncs marks each function whose body holds a wrapper, until a function that calls a wrapper function is marked too.
func (s *scanner) closeFuncs(bodies [][2]string) {
	for grew := true; grew; {
		grew = false

		for _, body := range bodies {
			if !s.funcs[body[0]] && holdsWrapper(body[1], s.funcs) {
				s.funcs[body[0]] = true
				grew = true
			}
		}
	}
}

func (s *scanner) noteCall(u unit, call *syntax.CallExpr, bodies *[][2]string) {
	for _, assign := range call.Assigns {
		s.noteAssign(u, assign)
	}

	if len(call.Args) > 1 && srcOf(u.src, call.Args[0]) == "alias" {
		name, value, found := strings.Cut(srcOf(u.src, call.Args[1]), "=")
		if found && aliasName.MatchString(name) {
			*bodies = append(*bodies, [2]string{name, strings.Trim(value, quoteCharacters)})
		}
	}
}

// noteAssign records a variable whose value holds a host wrapper, starts with a command wrapper or names a variable already recorded.
func (s *scanner) noteAssign(u unit, assign *syntax.Assign) {
	if assign.Name == nil || assign.Value == nil {
		return
	}

	if value := valueText(u.src, assign.Value); holdsWrapper(value, s.funcs) || startsWithWrapper(value) || s.namesRecorded(value) {
		s.vars[assign.Name.Value] = s.redefine(assign.Name.Value, value)
	}
}

// redefine replaces the name inside its own new value by the value it had before, up to the size cap, so `W="$W $W"` grows instead of naming itself.
func (s *scanner) redefine(name, value string) string {
	old, ok := s.vars[name]
	if !ok {
		return value
	}

	next := varRef.ReplaceAllStringFunc(value, func(ref string) string {
		match := varRef.FindStringSubmatch(ref)
		if match[1]+match[2] == name {
			return old
		}

		return ref
	})

	if len(next) > maxResolvedText {
		return value
	}

	return next
}

func startsWithWrapper(value string) bool {
	words := strings.Fields(value)

	return len(words) > 0 && isWrapper(path.Base(words[0]))
}

func (s *scanner) namesRecorded(value string) bool {
	return slices.ContainsFunc(varRef.FindAllStringSubmatch(value, -1), func(match []string) bool {
		_, ok := s.vars[match[1]+match[2]]

		return ok
	})
}
