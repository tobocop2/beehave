package check

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	assignWord         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	shellStringOption  = regexp.MustCompile(`^-[A-Za-z]*[cs][A-Za-z]*$`)
	shellCommandOption = regexp.MustCompile(`^-[A-Za-z]*c[A-Za-z]*$`)
)

const (
	timeoutName      = "timeout"
	gtimeoutName     = "gtimeout"
	sshName          = "ssh"
	cmdSudo          = "sudo"
	cmdEnv           = "env"
	cmdNice          = "nice"
	cmdExec          = "exec"
	cmdEval          = "eval"
	cmdFind          = "find"
	cmdNohup         = "nohup"
	cmdTime          = "time"
	valueOptionWidth = 2
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
	case cmdSudo, cmdEnv, cmdNice, timeoutName, gtimeoutName, "xargs", "sshpass", "stdbuf", "ionice", "busybox", cmdNohup, cmdTime, cmdExec, "command", "setsid":
		return true
	}

	return false
}

// takesValue reports whether the option of a wrapper takes its value from the next word.
func takesValue(name, option string) bool {
	switch name {
	case cmdSudo:
		return oneOf(option, "-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U")
	case cmdEnv:
		return oneOf(option, "-u", "-C", "-S", "--unset", "--chdir", "--split-string")
	case cmdNice:
		return oneOf(option, "-n", "--adjustment")
	case timeoutName, gtimeoutName:
		return oneOf(option, "-k", "-s", "--kill-after", "--signal")
	}

	return takesValueOfTool(name, option)
}

func takesValueOfTool(name, option string) bool {
	switch name {
	case "xargs":
		return oneOf(option, "-I", "-n", "-P", "-d", "-E", "-L", "-s", "-a")
	case "sshpass":
		return oneOf(option, "-p", "-f", "-d", "-P")
	case "stdbuf":
		return oneOf(option, "-i", "-o", "-e")
	case "ionice":
		return oneOf(option, "-c", "-n", "-t", "-p", "-P", "-u")
	}

	return false
}

func oneOf(word string, options ...string) bool {
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
	if isTimeout(name) {
		i++
	}

	return min(i, len(words)), true
}

// skipOptions returns the index after the options of the wrapper that start at i; an option that takes a value skips the next word too.
func skipOptions(words []string, i int, wrapper string) int {
	for i < len(words) && strings.HasPrefix(words[i], "-") && len(words[i]) > 1 {
		i += optionWidth(takesValue(wrapper, words[i]))
	}

	return i
}

func isTimeout(name string) bool {
	return name == timeoutName || name == gtimeoutName
}

func optionWidth(valued bool) int {
	if valued {
		return valueOptionWidth
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
