package check

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const (
	cmdEcho   = "echo"
	cmdPrintf = "printf"
	// minPrintfV is the word count of `printf -v NAME`.
	minPrintfV  = 3
	stepResult  = "STEP_RESULT"
	markerEnds  = "\"';&|><)"
	namePattern = `[A-Za-z_][A-Za-z0-9_]*`
	numPattern  = `-?(?:0[xX][0-9a-fA-F]+|[0-9]+)`
)

var (
	wholeNumber   = regexp.MustCompile(`^` + numPattern + `$`)
	wholeName     = regexp.MustCompile(`^` + namePattern + `$`)
	wholeVariable = regexp.MustCompile(`^\$\{?(` + namePattern + `)\}?$`)
	numberStart   = regexp.MustCompile(`^` + numPattern)
	variableStart = regexp.MustCompile(`^\$\{?` + namePattern + `\}?`)
	defaultStart  = regexp.MustCompile(`^\$\{(` + namePattern + `):?[-=]` + numPattern + `\}`)
	formatStart   = regexp.MustCompile(`^%[dis]`)
	keyValue      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*=`)
	rcMark        = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])rc=`)
)

// SourceReader returns the text of a file that a job sources, and false when the file cannot be read.
type SourceReader func(path string) (string, bool)

// assigns counts the assignments of each variable in a job, and what a sourced file can set.
type assigns struct {
	assigned, literal map[string]int
	dynamic           bool
	seen              map[string]bool
	jobDir            string
	read              SourceReader
	words             *scanner
}

func newAssigns(jobDir string, read SourceReader) *assigns {
	return &assigns{assigned: map[string]int{}, literal: map[string]int{}, seen: map[string]bool{}, jobDir: jobDir, read: read, words: newScanner()}
}

func (a *assigns) scan(units []unit) {
	for _, u := range units {
		syntax.Walk(u.file, func(node syntax.Node) bool {
			a.visit(u, node)

			return true
		})
	}
}

func (a *assigns) visit(u unit, node syntax.Node) {
	switch typed := node.(type) {
	case *syntax.Assign:
		a.assign(u, typed)
	case *syntax.BinaryArithm:
		a.binary(typed)
	case *syntax.UnaryArithm:
		a.unary(typed)
	case *syntax.LetClause:
		a.let(u, typed)
	case *syntax.ForClause:
		a.loop(typed)
	case *syntax.CallExpr:
		a.call(u, typed)
	}
}

func (a *assigns) binary(expr *syntax.BinaryArithm) {
	switch expr.Op { //nolint:exhaustive // only the assignment operators change a variable
	case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn, syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn, syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
		a.change(arithName(expr.X))
	}
}

func (a *assigns) unary(expr *syntax.UnaryArithm) {
	if expr.Op == syntax.Inc || expr.Op == syntax.Dec {
		a.change(arithName(expr.X))
	}
}

// loop counts the variable of a `for` or `select` loop as changed.
func (a *assigns) loop(clause *syntax.ForClause) {
	if iter, ok := clause.Loop.(*syntax.WordIter); ok {
		a.change(iter.Name.Value)
	}
}

func (a *assigns) change(name string) {
	if name != "" {
		a.assigned[name]++
	}
}

// arithName returns the name of the variable that an arithmetic expression changes.
func arithName(expr syntax.ArithmExpr) string {
	name := ""

	syntax.Walk(expr, func(node syntax.Node) bool {
		if lit, ok := node.(*syntax.Lit); ok && name == "" {
			name = lit.Value
		}

		return name == ""
	})

	return name
}

func (a *assigns) assign(u unit, assign *syntax.Assign) {
	if assign.Naked || assign.Name == nil {
		return
	}

	name := assign.Name.Value
	a.assigned[name]++

	if !assign.Append && assign.Value != nil && isNumber(a.words.literal(u, assign.Value)) {
		a.literal[name]++
	}
}

// let reads the quoted expressions of `let "R += 1"`, which the parser keeps as words.
func (a *assigns) let(u unit, clause *syntax.LetClause) {
	for _, expr := range clause.Exprs {
		word, ok := expr.(*syntax.Word)
		if !ok {
			continue
		}

		parsed, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Arithmetic(strings.NewReader(a.words.literal(u, word)))
		if err != nil {
			continue
		}

		syntax.Walk(parsed, func(node syntax.Node) bool {
			a.visit(u, node)

			return true
		})
	}
}

func (a *assigns) call(u unit, call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		return
	}

	args := a.literals(u, call.Args)

	switch args[0] {
	case "read", "mapfile", "readarray", "getopts":
		a.changeAll(args[1:])
	case cmdPrintf:
		if len(args) >= minPrintfV && args[1] == "-v" {
			a.change(args[2])
		}
	case "source", ".":
		if len(call.Args) > 1 {
			a.sourceFile(strings.NewReplacer(`"`, "", "'", "").Replace(srcOf(u.src, call.Args[1])))
		}
	case "eval":
		a.eval(strings.Join(args[1:], " "))
	}
}

func (a *assigns) literals(u unit, words []*syntax.Word) []string {
	out := make([]string, len(words))
	for i, word := range words {
		out[i] = a.words.literal(u, word)
	}

	return out
}

// changeAll counts each argument that is a variable name as changed.
func (a *assigns) changeAll(args []string) {
	for _, arg := range args {
		if isName(arg) {
			a.change(arg)
		}
	}
}

func (a *assigns) eval(body string) {
	if body == "" || strings.HasPrefix(strings.TrimLeft(body, `"'`), "$") {
		a.dynamic = true

		return
	}

	units, _ := parseUnits(body)
	a.scan(units)
}

// sourceFile scans the file that `source path` reads. A file nobody can read can set any variable.
func (a *assigns) sourceFile(file string) {
	if file == "" || strings.ContainsAny(file, "$`*?~") {
		a.dynamic = true

		return
	}

	if a.seen[file] {
		return
	}

	a.seen[file] = true

	if !a.scanFile(file) {
		a.dynamic = true
	}
}

// scanFile scans the first of the paths a sourced file can have that can be read, and reports whether one could.
func (a *assigns) scanFile(file string) bool {
	candidates := []string{file}
	if !strings.HasPrefix(file, "/") {
		candidates = append(candidates, a.jobDir+"/"+file)
	}

	for _, candidate := range candidates {
		if text, ok := a.read(candidate); ok {
			units, _ := parseUnits(text)
			a.scan(units)

			return true
		}
	}

	return false
}

// isLiteral reports whether the word is a number, or the `$NAME` of a variable with one assignment, a literal number.
func (a *assigns) isLiteral(word string) bool {
	word = strings.NewReplacer(`"`, "", "'", "").Replace(word)
	if isNumber(word) {
		return true
	}

	found := wholeVariable.FindStringSubmatch(word)
	if found == nil {
		return false
	}

	return !a.dynamic && a.assigned[found[1]] == 1 && a.literal[found[1]] == 1
}

func isNumber(text string) bool { return wholeNumber.MatchString(text) }

func isName(text string) bool { return wholeName.MatchString(text) }

// statusToken returns the literal status at the start of the text and the rest of the line.
func (a *assigns) statusToken(text string) (string, string, bool) {
	if found := defaultStart.FindStringSubmatch(text); found != nil {
		return a.defaultStatus(found[1], text, len(found[0]))
	}

	if number := numberStart.FindString(text); number != "" {
		return number, text[len(number):], true
	}

	if variable := variableStart.FindString(text); variable != "" {
		return variable, text[len(variable):], a.isLiteral(variable)
	}

	if format := formatStart.FindString(text); format != "" {
		return a.formatStatus(text[len(format):])
	}

	return "", "", false
}

// defaultStatus reads `${NAME:-N}`: the default is the status when no line sets NAME, or when NAME is itself a literal.
func (a *assigns) defaultStatus(name, text string, n int) (string, string, bool) {
	if a.dynamic || (a.assigned[name] > 0 && !a.isLiteral("$"+name)) {
		return "", "", false
	}

	return text[:n], text[n:], true
}

// formatStatus reads `rc=%d` in a printf: the status is the first argument after the format string.
func (a *assigns) formatStatus(after string) (string, string, bool) {
	arg := after
	if i := strings.IndexAny(after, `"'`); i >= 0 {
		arg = after[i+1:]
	}

	word := strings.TrimLeft(arg, blanks)
	if i := strings.IndexAny(word, " \t;&|"); i >= 0 {
		word = word[:i]
	}

	return word, after, a.isLiteral(word)
}

// markerTail reports whether the text after a status holds only key=value words, as `rc=0 gate=full`.
func markerTail(rest string) bool {
	if i := strings.IndexAny(rest, markerEnds); i >= 0 {
		rest = rest[:i]
	}

	rest = strings.NewReplacer(`\n`, " ", `\t`, " ").Replace(rest)

	for _, word := range blankFields(rest) {
		if !keyValue.MatchString(word) {
			return false
		}
	}

	return true
}

// fixedStatus returns the literal status that the line prints in a marker, or an empty string.
func (a *assigns) fixedStatus(line string) string {
	rest := line

	for {
		found := rcMark.FindStringIndex(rest)
		if found == nil {
			return ""
		}

		rest = rest[found[1]:]

		token := rest
		if token != "" && strings.IndexByte(quoteCharacters, token[0]) >= 0 {
			token = token[1:]
		}

		if status, tail, ok := a.statusToken(strings.TrimLeft(token, blanks)); ok && markerTail(tail) {
			return status
		}
	}
}

// printed collects the text of the commands that print, the here-documents and the here-strings of a job.
type printed struct {
	lines []string
	words *scanner
}

func (p *printed) collect(units []unit) {
	for _, u := range units {
		syntax.Walk(u.file, func(node syntax.Node) bool {
			switch typed := node.(type) {
			case *syntax.CallExpr:
				p.call(u, typed)
			case *syntax.Stmt:
				p.redirects(u, typed)
			}

			return true
		})
	}
}

// call keeps a print command. The text argument of any other command is read as shell, as `ssh host 'echo ...'` needs.
func (p *printed) call(u unit, call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		return
	}

	if name := p.words.literal(u, call.Args[0]); name == cmdEcho || name == cmdPrintf {
		p.lines = append(p.lines, flat(srcOf(u.src, call)))

		return
	}

	for _, arg := range call.Args[1:] {
		if text := p.words.literal(u, arg); strings.ContainsAny(text, blanks) {
			units, _ := parseUnits(text)
			p.collect(units)
		}
	}
}

func (p *printed) redirects(u unit, stmt *syntax.Stmt) {
	for _, redirect := range stmt.Redirs {
		switch {
		case redirect.Op == syntax.WordHdoc:
			p.lines = append(p.lines, flat(srcOf(u.src, redirect.Word)))
		case redirect.Hdoc != nil && runsShell(u, stmt):
			units, _ := parseUnits(srcOf(u.src, redirect.Hdoc))
			p.collect(units)
		case redirect.Hdoc != nil:
			p.lines = append(p.lines, strings.Split(srcOf(u.src, redirect.Hdoc), newline)...)
		}
	}
}

func runsShell(u unit, stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}

	name := path.Base(srcOf(u.src, call.Args[0]))

	return isShell(name) || name == "ssh"
}

// fixedStatusLines returns the FAIL lines for a marker that prints a literal exit status.
func fixedStatusLines(units []unit, jobDir string, read SourceReader) []string {
	counts := newAssigns(jobDir, read)
	counts.scan(units)

	prints := &printed{words: newScanner()}
	prints.collect(units)

	var lines []string

	for _, line := range prints.lines {
		if strings.Contains(line, skipMark) || strings.Contains(line, stepResult) {
			continue
		}

		if status := counts.fixedStatus(line); status != "" {
			lines = append(lines, fmt.Sprintf("%sthe job prints a fixed exit status (rc=%s): %s. Print the real one: rc=$? or a saved variable. A STEP_RESULT or GATE-SKIP line is not read", failPrefix, status, line))
		}
	}

	return lines
}
