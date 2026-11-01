package shell

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

var (
	assignWord         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	shellStringOption  = regexp.MustCompile(`^-[A-Za-z]*[cs][A-Za-z]*$`)
	shellCommandOption = regexp.MustCompile(`^-[A-Za-z]*c[A-Za-z]*$`)
	shortOption        = regexp.MustCompile(`^-[A-Za-z]+`)
)

// The names a check outside this package reads the same way the scan does.
const (
	TimeoutName      = "timeout"
	GtimeoutName     = "gtimeout"
	SSHName          = "ssh"
	CmdEval          = "eval"
	ValueOptionWidth = 2
)

const (
	cmdSudo   = "sudo"
	cmdEnv    = "env"
	cmdNice   = "nice"
	cmdExec   = "exec"
	cmdFind   = "find"
	cmdNohup  = "nohup"
	cmdTime   = "time"
	cmdSu     = "su"
	cmdDoas   = "doas"
	cmdFlock  = "flock"
	cmdChroot = "chroot"
	cmdWatch  = "watch"
)

const longCommandOption = "--command"

// The limits on the work of one scan behind a wrapper (see scanBehind).
// A command past a limit is reported as opaque, because the scan stops reading it.
const (
	MaxWordsBehind = 48
	MaxLinesBehind = 256
)

// isShell reports whether the command name is a shell that reads a script.
func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash":
		return true
	}

	return false
}

// isWrapper reports whether the command name is a wrapper that runs the command after its options.
func isWrapper(name string) bool {
	switch name {
	case cmdSudo, cmdEnv, cmdNice, TimeoutName, GtimeoutName, "xargs", "sshpass", "stdbuf", "ionice", "busybox", cmdNohup, cmdTime, cmdExec, "command", "setsid":
		return true
	}

	return false
}

// readsBehind reports whether the command name is a wrapper whose options the scan does not read.
// Its words are scanned as candidates instead (see scanBehind): a guard may be stricter than the real command, never looser.
func readsBehind(name string) bool {
	switch name {
	case cmdDoas, cmdFlock, cmdChroot, cmdWatch:
		return true
	case "runuser", "script", "systemd-run", "unbuffer", "taskset", "chrt", "nsenter", "unshare", "strace", "ltrace", "parallel", "pkexec", "sg", "setpriv":
		return true
	case "caffeinate", "firejail", "bwrap", "xvfb-run", "dbus-run-session", "proxychains", "proxychains4", "torsocks", "rlwrap", "cpulimit", "tmux", "screen", "dtach":
		return true
	}

	return false
}

// takesValue reports whether the option of a wrapper takes its value from the next word.
func takesValue(name, option string) bool {
	switch name {
	case cmdSudo:
		return OneOf(option, "-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U")
	case cmdEnv:
		return OneOf(option, "-u", "-C", "-S", "--unset", "--chdir", "--split-string")
	case cmdNice:
		return OneOf(option, "-n", "--adjustment")
	case TimeoutName, GtimeoutName:
		return OneOf(option, "-k", "-s", "--kill-after", "--signal")
	}

	return takesValueOfTool(name, option)
}

func takesValueOfTool(name, option string) bool {
	switch name {
	case "xargs":
		return OneOf(option, "-I", "-n", "-P", "-d", "-E", "-L", "-s", "-a")
	case "sshpass":
		return OneOf(option, "-p", "-f", "-d", "-P")
	case "stdbuf":
		return OneOf(option, "-i", "-o", "-e")
	case "ionice":
		return OneOf(option, "-c", "-n", "-t", "-p", "-P", "-u")
	case cmdExec:
		return option == "-a"
	}

	return false
}

// OneOf reports whether the word is one of the options.
func OneOf(word string, options ...string) bool {
	return slices.Contains(options, word)
}

// skipPrefix returns the index after the assignment or wrapper at i, or false when the word is the command.
func skipPrefix(words []string, i int) (int, bool) {
	if assignWord.MatchString(words[i]) {
		return i + 1, true
	}

	name := path.Base(words[i])
	if !isWrapper(name) {
		return i, false
	}

	i = skipOptions(words, i+1, name)
	if IsTimeout(name) {
		i++ // the duration
	}

	return min(i, len(words)), true
}

// skipOptions returns the index after the options of the wrapper that start at i; an option that takes a value skips the next word too.
func skipOptions(words []string, i int, wrapper string) int {
	for i < len(words) && strings.HasPrefix(words[i], "-") && len(words[i]) > 1 {
		i += optionWidth(takesNextWord(wrapper, words[i]))
	}

	return i
}

// takesNextWord reports whether the option takes its value from the next word.
// A short cluster such as `-dn` ends in the option that takes the value; a value option inside the cluster owns the rest of the cluster (`-dn2`).
func takesNextWord(wrapper, option string) bool {
	if takesValue(wrapper, option) {
		return true
	}

	if len(option) < 3 || strings.HasPrefix(option, dashDash) {
		return false
	}

	for k := 1; k < len(option); k++ {
		if takesValue(wrapper, "-"+option[k:k+1]) {
			return k == len(option)-1
		}
	}

	return false
}

// IsTimeout reports whether the command name is a timeout wrapper.
func IsTimeout(name string) bool {
	return name == TimeoutName || name == GtimeoutName
}

func optionWidth(valued bool) int {
	if valued {
		return ValueOptionWidth
	}

	return 1
}

// scriptStart returns the index of the script file a shell at i runs, or i when it runs a string or stdin.
func scriptStart(words []string, i int) int {
	j := i + 1
	for j < len(words) && (strings.HasPrefix(words[j], "-") || strings.HasPrefix(words[j], "+")) && len(words[j]) > 1 {
		if words[j] == dashDash {
			j++

			break
		}

		if shellStringOption.MatchString(words[j]) {
			return i
		}

		j += optionWidth(slices.Contains([]string{"-o", "+o", "-O", "+O"}, words[j]))
	}

	if j < len(words) {
		return j
	}

	return i
}

// headOf drops the assignments, the wrappers and a leading shell that runs a script file; it returns the command and the dropped words.
func headOf(words []string) ([]string, []string) {
	i := 0

	for i < len(words) {
		next, ok := skipPrefix(words, i)
		if !ok {
			break
		}

		i = next
	}

	if i+1 < len(words) && isShell(path.Base(words[i])) {
		i = scriptStart(words, i)
	}

	return words[i:], words[:i]
}

// hidesRule reports whether a command word that holds an expansion hides a command a rule would refuse: a git staging or a delete in the words after it.
// A host wrapper and a positional parameter hide nothing. A function name never holds `$`, so a wrapper function needs no case.
func hidesRule(head []string) bool {
	word := head[0]
	if !strings.Contains(word, "$") || isPositional(word) || isWrapperWord(word) {
		return false
	}

	if bulkHit(append([]string{"git"}, head[1:]...)) != "" {
		return true
	}

	if deleteHit(head[1:]) {
		return stopsLaptop(head[1:])
	}

	return stopsLaptopAsRM(head[1:])
}

// stopsLaptopAsRM reports whether the words would be a delete that stops the laptop if the command word were `rm`.
// The variable can be a wrapper before the delete (`deleteHit` above) or the delete itself. Only words with a recursive or force option count, so `$CC -o x x.c` stays quiet.
func stopsLaptopAsRM(args []string) bool {
	return slices.ContainsFunc(args, isDeleteOption) && stopsLaptop(append([]string{"rm"}, args...))
}

// isDeleteOption reports whether the word is `--recursive`, `--force` or a short cluster of `rm` letters that holds `r`, `R` or `f`.
func isDeleteOption(word string) bool {
	if word == "--recursive" || word == "--force" {
		return true
	}

	letters, short := strings.CutPrefix(word, "-")

	return short && strings.Trim(letters, "rRfdvi") == "" && strings.ContainsAny(letters, "rRf")
}

func isPositional(word string) bool {
	return len(word) == 2 && strings.HasPrefix(word, "$") && strings.ContainsRune("@*123456789", rune(word[1]))
}

// splitsString reports whether the dropped words of an env command split a string into a command (`-S`).
func splitsString(prefix []string) bool {
	return slices.ContainsFunc(prefix, func(word string) bool {
		return word == "-S" || strings.HasPrefix(word, "--split-string")
	})
}

// scanBehind reads the words after `su` or a wrapper in readsBehind without reading its options.
// The real command is some suffix of the words whatever the option grammar, so each suffix is judged as a command.
// A word that holds a command line is judged as a shell string: the value of `-c` or `--command`, and a word with a space.
func (s *scanner) scanBehind(cmd *command, remote bool) {
	rest := cmd.head[1:]
	if len(rest) > MaxWordsBehind {
		s.add(model.FindingOpaque, cmd.head[:1])

		return
	}

	s.scanSuffixes(cmd, rest, remote)
	s.scanLines(cmd, rest, remote)
}

// scanSuffixes judges each suffix of the words behind a wrapper as a command.
func (s *scanner) scanSuffixes(cmd *command, rest []string, remote bool) {
	for i := range rest {
		head, prefix := headOf(rest[i:])
		if len(head) > 0 && !readsAllBehind(path.Base(head[0])) { // a nested wrapper's suffixes are already in this loop
			s.scanCommand(&command{head: head, prefix: append(slices.Clone(cmd.prefix), prefix...)}, remote)
		}
	}
}

// scanLines judges each command line behind a wrapper as a shell string, up to MaxLinesBehind lines.
func (s *scanner) scanLines(cmd *command, rest []string, remote bool) {
	read := 0

	for i := range rest {
		for _, text := range commandLines(rest, i) {
			if read++; read > MaxLinesBehind {
				s.add(model.FindingOpaque, cmd.head[:1])

				return
			}

			s.scanOnce(text, remote)
		}
	}
}

// scanOnce scans a shell string unless this scan already read it in the same folder.
// The folder is part of the key because a relative path in the string means a different file after a `cd`.
func (s *scanner) scanOnce(text string, remote bool) {
	key := fmt.Sprintf("%t %q %q", remote, s.cwd, text)
	if s.lines[key] {
		return
	}

	if s.lines == nil {
		s.lines = map[string]bool{}
	}

	s.lines[key] = true
	s.scan(text, remote)
}

func readsAllBehind(name string) bool {
	return name == cmdSu || readsBehind(name)
}

// commandLines returns the shell strings that the word at i can be: a `--command=` value, the word after a command option, a word with a space, and the text after each leading letter of a short option word (`-c'x y'` is one word `-cx y`).
// The words after the option word follow the text of a short option, because an unquoted value (`-crm f`) spreads over them.
func commandLines(words []string, i int) []string {
	word := words[i]
	if text, ok := strings.CutPrefix(word, longCommandOption+"="); ok {
		return []string{text}
	}

	var lines []string

	afterOption := i > 0 && (words[i-1] == longCommandOption || shellCommandOption.MatchString(words[i-1]))
	if afterOption || strings.ContainsAny(word, " \t\n") {
		lines = append(lines, word)
	}

	for end := range len(shortOption.FindString(word)) {
		if end > 0 && end+1 < len(word) {
			lines = append(lines, strings.Join(append([]string{word[end+1:]}, words[i+1:]...), " "))
		}
	}

	return lines
}
