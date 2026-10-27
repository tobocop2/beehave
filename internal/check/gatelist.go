package check

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const (
	gateColumns = 2
	skipMark    = "GATE-SKIP"
	failPrefix  = "FAIL: "
)

var (
	gateHeading   = regexp.MustCompile(`^## Gate steps[ \t]*$`)
	derivedLine   = regexp.MustCompile(`Derived from: *[^ ]`)
	pinnedLine    = regexp.MustCompile(` at [0-9a-f]{7}`)
	separatorCell = regexp.MustCompile(`^:?-+:?$`)
	stepName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]*$`)
	seeCommand    = regexp.MustCompile(`^(?:command: *)?see `)
	slotPart      = regexp.MustCompile(`<[^>]*>`)
)

// GateStep is one row of the gate list: a step name and the command that runs it.
type GateStep struct {
	Name, Command string
}

// gateList is the read form of the `## Gate steps` section.
type gateList struct {
	steps    []GateStep
	names    map[string]bool
	unstated map[string]bool
	errors   []string
}

func (g *gateList) has(name string) bool {
	return g.names[name]
}

// addRow reads one table row of the gate list. A header row and a separator row add nothing.
func (g *gateList) addRow(line, name string) {
	cells := cleanCells(line)
	first := ""

	if len(cells) > 0 {
		first = cells[0]
	}

	switch {
	case strings.EqualFold(first, "name") || separatorCell.MatchString(first):
	case len(cells) != gateColumns:
		g.errors = append(g.errors, fmt.Sprintf(`%s: the gate row "%s" has %d columns, expected name and command`, name, first, len(cells)))
	default:
		g.addStep(cells[0], cells[1], name)
	}
}

// cleanCells splits a row and removes the backticks around each cell.
func cleanCells(line string) []string {
	cells := splitRow(line)
	for i, cell := range cells {
		cells[i] = strings.Trim(strings.ReplaceAll(cell, "`", ""), blanks)
	}

	return cells
}

func (g *gateList) addStep(step, command, name string) {
	switch {
	case !stepName.MatchString(step):
		g.errors = append(g.errors, fmt.Sprintf(`%s: "%s" is not a step name`, name, step))
	case command == "":
		g.errors = append(g.errors, fmt.Sprintf(`%s: the step %s has no command. Write the command, or "command: see <workflow file>"`, name, step))
	case g.has(step):
		g.errors = append(g.errors, fmt.Sprintf("%s: the step %s is listed twice", name, step))
	default:
		g.steps = append(g.steps, GateStep{step, command})
		g.names[step] = true

		if seeCommand.MatchString(command) {
			g.unstated[step] = true
		}
	}
}

// gateScan follows the walk over the lines of the gate list: which section it is in and what that section holds.
type gateScan struct {
	on              bool
	sections        int
	derived, pinned bool
}

func (s *gateScan) read(list *gateList, line, name string) {
	if strings.HasPrefix(line, headingPrefix) {
		s.on = gateHeading.MatchString(line)
		if s.on {
			s.sections++
		}

		return
	}

	if !s.on {
		return
	}

	s.derive(line)

	if tableLine.MatchString(line) {
		list.addRow(line, name)
	}
}

func (s *gateScan) derive(line string) {
	if derivedLine.MatchString(line) {
		s.derived = true
		s.pinned = s.pinned || pinnedLine.MatchString(line)
	}
}

func (s *gateScan) errors(g *gateList, name string) []string {
	switch {
	case s.sections == 0:
		return []string{name + ` has no "## Gate steps" section. Add it: one "| name | command |" row for each step, derived from the CI workflow of the repository`}
	case s.sections > 1:
		return []string{fmt.Sprintf(`%s has %d "## Gate steps" sections. The gate list has one home`, name, s.sections)}
	case !s.derived:
		return []string{name + `: the "## Gate steps" section has no "Derived from: <workflow file and jobs>, at <commit>" line`}
	case !s.pinned:
		return []string{name + `: the "Derived from:" line names no commit. End it with "at <commit>", the base tip the workflow files were read at`}
	case len(g.steps) == 0:
		return []string{name + `: the "## Gate steps" section has no rows`}
	}

	return nil
}

func readGateList(text, name string) *gateList {
	list, scan := &gateList{names: map[string]bool{}, unstated: map[string]bool{}}, &gateScan{}

	for _, line := range recordsOf(text) {
		scan.read(list, line, name)
	}

	list.errors = append(list.errors, scan.errors(list, name)...)

	return list
}

// GateSteps returns the steps of the gate list, or a refusal that names each reason the list cannot be read.
func GateSteps(text, name string) ([]GateStep, error) {
	list := readGateList(text, name)
	if len(list.errors) > 0 {
		return nil, listRefusal(list.errors, nil)
	}

	return list.steps, nil
}

func listRefusal(errors, lines []string) *Refusal {
	messages := make([]string, len(errors))
	for i, message := range errors {
		messages[i] = failPrefix + message
	}

	return &Refusal{Message: strings.Join(messages, newline), Lines: lines}
}

// gateScript is what the gate check reads from a job: its commands, its declared skips and their reasons.
type gateScript struct {
	calls     []string
	skipNames []string
	skipTool  map[string]string
	verified  bool
	partial   bool
	fails     []string
}

// flat joins the lines of a command and folds each run of blanks to one blank.
func flat(text string) string {
	return strings.Join(blankFields(strings.ReplaceAll(text, "\\\n", " ")), " ")
}

// readGateScript reads the job as the commands a line holds: each simple command, each pipeline or list, each subshell, and each line of a here-document.
func readGateScript(units []unit) *gateScript {
	script := &gateScript{skipTool: map[string]string{}}

	for _, u := range units {
		script.readUnit(u)
	}

	return script
}

func (s *gateScript) readUnit(u unit) {
	lines, stmts := s.scan(u)

	for _, kept := range lines.outermost(append(lines.stmtSpans(stmts), lines.chainSpans(stmts)...)) {
		s.addCall(flat(u.src[kept.start:kept.end]))
	}
}

// scan walks a unit once. It reads each simple command and each here-document body, and returns the statements with the offsets of the unit.
func (s *gateScript) scan(u unit) (*unitLines, []*syntax.Stmt) {
	var stmts []*syntax.Stmt

	lines := &unitLines{skips: markOffsets(u.src), ends: map[*syntax.Stmt]syntax.Pos{}}

	syntax.Walk(u.file, func(node syntax.Node) bool {
		if stmt, ok := node.(*syntax.Stmt); ok {
			stmts = append(stmts, stmt)
		} else {
			s.visit(u, lines, node)
		}

		return true
	})

	slices.Sort(lines.heredocs)

	lines.measure(stmts)

	return lines, stmts
}

// visit reads a simple command, or notes a here-document and reads its body.
func (s *gateScript) visit(u unit, lines *unitLines, node syntax.Node) {
	switch typed := node.(type) {
	case *syntax.CallExpr:
		s.track(flat(srcOf(u.src, typed)))
	case *syntax.Redirect:
		if typed.Hdoc != nil {
			lines.heredocs = append(lines.heredocs, int(typed.Pos().Offset()))
			s.addBody(srcOf(u.src, typed.Hdoc))
		}
	}
}

// span is a range of bytes in the source of a unit.
type span struct{ start, end int }

// unitLines answers questions about a unit by byte offset, so that no answer walks a statement again.
type unitLines struct {
	heredocs []int
	skips    []int
	ends     map[*syntax.Stmt]syntax.Pos
}

// measure reads the end of each statement. A statement comes after the statement that holds it, so each end is read after the ends it depends on.
func (l *unitLines) measure(stmts []*syntax.Stmt) {
	for i := len(stmts) - 1; i >= 0; i-- {
		l.ends[stmts[i]] = l.closeOf(stmts[i])
	}
}

// closeOf returns where a statement ends. The parser computes the end of `time a | time b | ...` by a walk down the whole chain, so the end of each statement is built from the end of the statement at its tail, which is known already.
func (l *unitLines) closeOf(stmt *syntax.Stmt) syntax.Pos {
	var tail *syntax.Stmt

	switch typed := stmt.Cmd.(type) {
	case *syntax.BinaryCmd:
		tail = typed.Y
	case *syntax.TimeClause:
		tail = typed.Stmt
	case *syntax.CoprocClause:
		tail = typed.Stmt
	case *syntax.FuncDecl:
		tail = typed.Body
	}

	if tail == nil || stmt.Semicolon.IsValid() {
		return stmt.End()
	}

	// The parser keeps a redirect on the last simple command, never on a statement of this kind.
	return l.ends[tail]
}

func (l *unitLines) stmtSpanOf(stmt *syntax.Stmt) span {
	return span{int(stmt.Pos().Offset()), int(l.ends[stmt].Offset())}
}

// markOffsets returns the offset of each skip mark in the text.
func markOffsets(text string) []int {
	var offsets []int

	for from := 0; ; {
		at := strings.Index(text[from:], skipMark)
		if at < 0 {
			return offsets
		}

		offsets = append(offsets, from+at)
		from += at + 1
	}
}

// within reports whether a sorted offset list holds an offset that starts at or after from and ends at or before the limit.
func within(offsets []int, from, limit, width int) bool {
	at, _ := slices.BinarySearch(offsets, from)

	return at < len(offsets) && offsets[at]+width <= limit
}

// holdsHeredoc reports whether the range holds a here-document, whose body is read line by line instead.
func (l *unitLines) holdsHeredoc(s span) bool {
	at, _ := slices.BinarySearch(l.heredocs, s.start)

	return at < len(l.heredocs) && l.heredocs[at] < s.end
}

func spanOf(node syntax.Node) span {
	return span{int(node.Pos().Offset()), int(node.End().Offset())}
}

// stmtSpans returns the range of each statement that can hold a whole gate command. A simple command, a list, a pipeline and a subshell keep every line.
// Any other command keeps its keyword when it sits on one line.
func (l *unitLines) stmtSpans(stmts []*syntax.Stmt) []span {
	var spans []span

	for _, stmt := range stmts {
		if whole, ok := l.stmtSpan(stmt); ok {
			spans = append(spans, whole)
		}
	}

	return spans
}

func (l *unitLines) stmtSpan(stmt *syntax.Stmt) (span, bool) {
	whole := l.stmtSpanOf(stmt)

	switch typed := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		if l.holdsHeredoc(whole) {
			return spanOf(typed), true
		}

		return whole, true
	case *syntax.BinaryCmd, *syntax.Subshell:
		return whole, !l.holdsHeredoc(whole)
	case nil:
		return whole, false
	}

	return whole, stmt.Pos().Line() == l.ends[stmt].Line() && !l.holdsHeredoc(whole)
}

// chainSpans returns the range of each run of statements that share a line, such as `a; b`.
func (l *unitLines) chainSpans(stmts []*syntax.Stmt) []span {
	var spans []span

	for i, last := range l.chainEnds(stmts) {
		first := stmts[i]

		if last != i && !l.holdsHeredoc(l.stmtSpanOf(first)) && !l.holdsHeredoc(l.stmtSpanOf(stmts[last])) {
			spans = append(spans, span{int(first.Pos().Offset()), int(l.ends[stmts[last]].Offset())})
		}
	}

	return spans
}

// chainEnds returns, for each statement, the index of the last statement of the run that starts there. A statement that nests inside the one before it, or sits on a later line, is not a follower. The run of a statement ends where the run of its follower ends.
func (l *unitLines) chainEnds(stmts []*syntax.Stmt) []int {
	ends := make([]int, len(stmts))

	for i := len(stmts) - 1; i >= 0; i-- {
		ends[i] = i

		rest := stmts[i+1:]
		after := l.ends[stmts[i]]
		at, _ := slices.BinarySearchFunc(rest, int(after.Offset()), func(stmt *syntax.Stmt, offset int) int {
			return cmp.Compare(int(stmt.Pos().Offset()), offset)
		})

		if at < len(rest) && rest[at].Pos().Line() == after.Line() {
			ends[i] = ends[i+1+at]
		}
	}

	return ends
}

// outermost drops each range that holds a skip mark, and each range that a longer range already holds. The text kept for the step rules is then as long as the unit.
func (l *unitLines) outermost(spans []span) []span {
	spans = slices.DeleteFunc(spans, func(s span) bool { return within(l.skips, s.start, s.end, len(skipMark)) })

	slices.SortFunc(spans, func(a, b span) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(b.end, a.end))
	})

	kept, reach := spans[:0], -1

	for _, s := range spans {
		if s.end > reach {
			kept, reach = append(kept, s), s.end
		}
	}

	return kept
}

// addBody reads each line of a here-document body as a line of the job. A comment line is not a command.
func (s *gateScript) addBody(body string) {
	for _, line := range strings.Split(strings.ReplaceAll(body, "\\\n", " "), newline) {
		if line = flat(line); line != "" && !strings.HasPrefix(line, "#") {
			s.track(line)
			s.addCall(line)
		}
	}
}

// addCall keeps the text of a command for the step rules. A line that declares a skip runs nothing.
func (s *gateScript) addCall(text string) {
	if !strings.Contains(text, skipMark) {
		s.calls = append(s.calls, text)
	}
}

func (s *gateScript) track(text string) {
	s.verified = s.verified || strings.Contains(text, "command -v ")
	s.partial = s.partial || strings.Contains(text, "gate=partial")

	if !strings.Contains(text, skipMark) {
		return
	}

	if name, reason, ok := skipMatch(text); ok {
		s.declare(name, strings.Trim(strings.NewReplacer(`"`, "", "'", "").Replace(reason), blanks))
	} else {
		s.fails = append(s.fails, "a GATE-SKIP line names no step: "+text)
	}
}

// declare records a skip, and the defects of its reason.
func (s *gateScript) declare(name, reason string) {
	s.skipNames = append(s.skipNames, name)

	switch {
	case reason == "":
		s.fails = append(s.fails, fmt.Sprintf("GATE-SKIP %s has no reason. Write GATE-SKIP %s: <reason>", name, name))
	case namesMissingTool(reason):
		s.skipTool[name] = reason
	}
}

func namesMissingTool(reason string) bool {
	lower := strings.ToLower(reason)

	for _, word := range []string{"missing", "not installed", "not found", "does not have", "absent", "unavailable"} {
		if strings.Contains(lower, word) {
			return true
		}
	}

	return false
}

// skipMatch finds the first `GATE-SKIP <name>:` in the text and returns the name and the text after the colon.
func skipMatch(text string) (string, string, bool) {
	for from := 0; ; {
		at := strings.Index(text[from:], skipMark)
		if at < 0 {
			return "", "", false
		}

		start := from + at + len(skipMark)
		rest := strings.TrimLeft(text[start:], " ")

		if len(rest) < len(text[start:]) {
			if end := strings.IndexAny(rest, ": "); end > 0 && rest[end] == ':' {
				return rest[:end], rest[end+1:], true
			}
		}

		from = start
	}
}

// hasCommand reports whether one command holds every literal part of the gate command, in order.
func (s *gateScript) hasCommand(command string) bool {
	parts := slotPart.Split(command, -1)
	for i, part := range parts {
		parts[i] = strings.Trim(part, blanks)
	}

	for _, call := range s.calls {
		if holdsInOrder(call, parts) {
			return true
		}
	}

	return false
}

func holdsInOrder(text string, parts []string) bool {
	pos := 0

	for _, part := range parts {
		if part == "" {
			continue
		}

		at := strings.Index(text[pos:], part)
		if at < 0 {
			return false
		}

		pos += at + len(part)
	}

	return true
}

// hasName reports whether one command holds `name=<step>` as a whole word.
func (s *gateScript) hasName(step string) bool {
	return slices.ContainsFunc(s.calls, func(call string) bool {
		return holdsToken(call, "name="+step, false, func(char byte) bool { return isAlnum(char) || strings.IndexByte(":_.-", char) >= 0 })
	})
}

func isAlnum(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

// holdsToken reports whether the text holds the token with no word byte after it, and, when lead is set, none before it.
func holdsToken(text, token string, lead bool, word func(byte) bool) bool {
	for from := 0; from < len(text); {
		at := strings.Index(text[from:], token)
		if at < 0 {
			return false
		}

		at += from
		end := at + len(token)

		if (!lead || at == 0 || !word(text[at-1])) && (end == len(text) || !word(text[end])) {
			return true
		}

		from = at + 1
	}

	return false
}

func (s *gateScript) skipped(step string) bool {
	for _, name := range s.skipNames {
		if name == step {
			return true
		}
	}

	return false
}

// GateResult holds the lines of a gate comparison and the counts for its summary.
type GateResult struct {
	Lines                       []string
	steps, run, skips, unstated int
}

// OK reports whether no line is a defect.
func (r GateResult) OK() bool {
	for _, line := range r.Lines {
		if strings.HasPrefix(line, failPrefix) {
			return false
		}
	}

	return true
}

// Summary is the line printed for a job that passes.
func (r GateResult) Summary() string {
	return fmt.Sprintf("check-job: OK (steps %d, run %d, declared skips %d, unstated %d)", r.steps, r.run, r.skips, r.unstated)
}

// stepLines returns the UNSTATED and FAIL lines of the steps, the count of steps that run and the count of unstated steps.
func stepLines(list *gateList, script *gateScript) ([]string, int, int) {
	var lines []string

	run, unstated := 0, 0

	for _, step := range list.steps {
		if list.unstated[step.Name] {
			unstated++

			lines = append(lines, fmt.Sprintf("UNSTATED %s: the gate list states no command (%s). Read it from that file. Nothing here compared it with the job.", step.Name, step.Command))
		}

		switch {
		case script.runs(list, step):
			run++
		case !script.skipped(step.Name):
			lines = append(lines, missingStep(list, step))
		}
	}

	return lines, run, unstated
}

// runs reports whether the job runs the step: an unstated step needs its name, any other its command.
func (s *gateScript) runs(list *gateList, step GateStep) bool {
	if list.unstated[step.Name] {
		return s.hasName(step.Name)
	}

	return s.hasCommand(step.Command)
}

func missingStep(list *gateList, step GateStep) string {
	if list.unstated[step.Name] {
		return fmt.Sprintf("%sthe job names no step %s. Run it and print STEP_RESULT name=%s rc=<n>, or print GATE-SKIP %s: <reason>", failPrefix, step.Name, step.Name, step.Name)
	}

	return fmt.Sprintf("%sthe job does not run the step %s (%s). Add the command, or print GATE-SKIP %s: <reason>", failPrefix, step.Name, step.Command, step.Name)
}

// skipLines returns the FAIL lines for declared skips.
func skipLines(list *gateList, script *gateScript, listPath string) []string {
	var lines []string

	for _, name := range script.skipNames {
		if !list.has(name) {
			lines = append(lines, fmt.Sprintf("%sGATE-SKIP %s names no step of the gate list in %s", failPrefix, name, listPath))
		}

		if reason, tool := script.skipTool[name]; tool && !script.verified {
			lines = append(lines, fmt.Sprintf("%sGATE-SKIP %s names a missing tool (%s). Verify it in the same job with command -v, and print the result", failPrefix, name, reason))
		}
	}

	if len(script.skipNames) > 0 && !script.partial {
		lines = append(lines, failPrefix+"the job can skip a step, and its final marker does not say so. End it with <MARKER> rc=<n> gate=partial:<count>")
	}

	return lines
}

// GateJob compares a job script with the gate list. A gate list that cannot be read is a refusal that keeps the defects found in the job first. The caller adds the lines of JobScript.
// The script and the list are Go strings. A byte that is not UTF-8 stays as it is in a line; the caller converts the lines it prints.
func GateJob(script, listText, listPath, jobDir string, source SourceReader) (GateResult, error) {
	units, _ := parseUnits(script) // JobScript reports the lines that do not parse
	read := readGateScript(units)
	list := readGateList(listText, listPath)

	fails := make([]string, len(read.fails))
	for i, message := range read.fails {
		fails[i] = failPrefix + message
	}

	if len(list.errors) > 0 {
		return GateResult{}, listRefusal(list.errors, fails)
	}

	steps, run, unstated := stepLines(list, read)
	result := GateResult{steps: len(list.steps), run: run, skips: len(read.skipNames), unstated: unstated}
	result.Lines = append(append(append(fails, steps...), fixedStatusLines(units, jobDir, source)...), skipLines(list, read, listPath)...)

	return result, nil
}
