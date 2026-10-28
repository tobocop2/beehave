package shell

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	wordToken    = regexp.MustCompile(`\$\{?\w+\}?|[^\s;&|(){}"'` + "`" + `]+`)
	variableWord = regexp.MustCompile(`^\$\{?(\w+)\}?$`)
	nameParts    = regexp.MustCompile(`[^a-z0-9]+`)
)

// isWrapperWord reports whether the command word names a host wrapper: a variable or a `.sh` script with `ssh` or `box` as a whole name part.
func isWrapperWord(word string) bool {
	if strings.HasPrefix(word, "$") {
		match := variableWord.FindStringSubmatch(word)
		if match == nil {
			return false
		}

		parts := strings.Split(strings.ToLower(match[1]), "_")

		return slices.Contains([]string{SSHName, "box"}, parts[len(parts)-1])
	}

	base := strings.ToLower(path.Base(word))
	if !strings.HasSuffix(base, ".sh") {
		return false
	}

	return slices.ContainsFunc(nameParts.Split(strings.TrimSuffix(base, ".sh"), -1), func(part string) bool {
		return part == SSHName || part == "box"
	})
}

// holdsWrapper reports whether a word of the text is a host wrapper, ssh itself or a known wrapper function.
func holdsWrapper(text string, funcs map[string]bool) bool {
	return slices.ContainsFunc(wordToken.FindAllString(text, -1), func(token string) bool {
		return isWrapperWord(token) || path.Base(token) == SSHName || funcs[token]
	})
}

// sshOptionWidth returns 2 when the ssh option word takes its value from the next word, else 1.
func sshOptionWidth(word string) int {
	for index, letter := range word[1:] {
		if strings.ContainsRune("BbcDEeFIiJLlmOopQRSWw", letter) {
			return optionWidth(index+2 == len(word))
		}
	}

	return 1
}

func gcloudPositionals(rest []string) []string {
	var pos []string

	for i := 0; i < len(rest); {
		word := rest[i]

		switch {
		case word == dashDash:
			return pos
		case strings.HasPrefix(word, "-"):
			i += optionWidth(gcloudValueFlag(word))
		default:
			pos = append(pos, word)
			i++
		}
	}

	return pos
}

func gcloudValueFlag(flag string) bool {
	switch flag {
	case "--project", "--account", "--zone", "--configuration", "--format", "--impersonate-service-account", "--billing-project", "--verbosity":
		return true
	}

	return false
}

// gcloudRemote returns the remote command text of a gcloud ssh command.
func gcloudRemote(words []string) (string, bool) {
	rest := words[1:]
	if !slices.Contains(gcloudPositionals(rest), SSHName) {
		return "", false
	}

	return strings.Join(gcloudCommand(rest), " "), true
}

// gcloudCommand returns the words after `--` and the values of --command.
func gcloudCommand(rest []string) []string {
	var text []string

	for i, word := range rest {
		switch {
		case word == dashDash:
			return append(text, rest[i+1:]...)
		case strings.HasPrefix(word, "--command="):
			text = append(text, strings.TrimPrefix(word, "--command="))
		case word == "--command" && i+1 < len(rest):
			text = append(text, rest[i+1])
		}
	}

	return text
}

// sshRemote returns the remote command text of an ssh command: the words after the host.
func sshRemote(words []string) string {
	rest := words[1:]
	host := false

	for i := 0; i < len(rest); {
		word := rest[i]

		switch {
		case word == dashDash:
			i++
		case strings.HasPrefix(word, "-") && len(word) > 1:
			i += sshOptionWidth(word)
		case !host:
			host = true
			i++
		default:
			return strings.Join(rest[i:], " ")
		}
	}

	return ""
}

// remoteOf returns the remote command text when the words, which are not empty, start a remote command.
func remoteOf(words []string, funcs map[string]bool) (string, bool) {
	base := path.Base(words[0])

	switch {
	case base == "gcloud":
		return gcloudRemote(words)
	case base == SSHName:
		return sshRemote(words), true
	case isWrapperWord(words[0]) || funcs[base]:
		k := 1
		for k < len(words) && strings.HasPrefix(words[k], "-") && len(words[k]) > 1 {
			k++
		}

		return strings.Join(words[k:], " "), true
	}

	return "", false
}

// stdinIsScript reports whether the remote command reads its stdin as a script: no command, or a bare shell.
func stdinIsScript(remote string) bool {
	words := strings.Fields(remote)
	if len(words) == 0 {
		return true
	}

	return isShell(path.Base(words[0])) && !slices.ContainsFunc(words[1:], func(word string) bool {
		return !strings.HasPrefix(word, "-")
	})
}
