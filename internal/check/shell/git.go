package shell

import (
	"path"
	"slices"
	"strings"
)

const (
	privateIndex = "GIT_INDEX_FILE="
	dashDash     = "--"
)

// pathspecAll returns the first pathspec that names every file, or that holds an operator expansion whose value is unknown.
func pathspecAll(args []string) string {
	for _, arg := range args {
		switch arg {
		case ".", "./", "*", ":/":
			return arg
		}

		if strings.Contains(arg, "${") {
			return arg
		}
	}

	return ""
}

// flagSet splits the arguments of git add into short letters, long options and positional words.
type flagSet struct {
	letters string
	longs   []string
	pos     []string
}

func readFlags(args []string) flagSet {
	var set flagSet

	afterDashes := false

	for _, arg := range args {
		switch {
		case afterDashes || !strings.HasPrefix(arg, "-") || arg == "-":
			set.pos = append(set.pos, arg)
		case arg == dashDash:
			afterDashes = true
		case strings.HasPrefix(arg, dashDash):
			set.longs = append(set.longs, arg)
		default:
			set.letters += arg[1:]
		}
	}

	return set
}

func (f flagSet) has(letter, long string) bool {
	return strings.Contains(f.letters, letter) || slices.Contains(f.longs, long)
}

// hasAbbrev is has for an option git also accepts as a unique prefix, such as `--upd` for `--update`.
func (f flagSet) hasAbbrev(letter, long string) bool {
	return strings.Contains(f.letters, letter) || slices.ContainsFunc(f.longs, func(given string) bool {
		return len(given) > len(dashDash) && strings.HasPrefix(long, given)
	})
}

func addHit(sub string, args []string) string {
	set := readFlags(args)

	switch {
	case set.has("n", "--dry-run"):
		return ""
	case set.hasAbbrev("A", "--all"):
		return "git " + sub + " -A"
	case set.hasAbbrev("u", "--update"):
		return "git " + sub + " -u"
	case set.has("N", "--intent-to-add"):
		return ""
	}

	return pathspecHit("git "+sub, set.pos)
}

// pathspecHit returns the bulk hit text for a pathspec that names every file, or an empty string.
func pathspecHit(prefix string, pos []string) string {
	if spec := pathspecAll(pos); spec != "" {
		return prefix + " " + spec
	}

	return ""
}

// commitValueLong lists the long commit options whose next word is a value.
func commitValueLong(option string) bool {
	switch option {
	case "--message", "--file", "--author", "--date", "--reuse-message", "--reedit-message", "--template", "--cleanup", "--fixup", "--squash":
		return true
	}

	return false
}

// commitShort reads a short option cluster: whether it holds -a, and how many words it uses.
func commitShort(cluster string) (bool, int) {
	for index, letter := range cluster[1:] {
		switch {
		case letter == 'a':
			return true, 1
		case strings.ContainsRune("mFCct", letter):
			return false, optionWidth(index+2 == len(cluster))
		}
	}

	return false, 1
}

// commitWord reads one word of git commit: the text of a bulk hit, the number of words to skip, and whether the word is a pathspec.
func commitWord(arg string) (string, int, bool) {
	switch {
	case !strings.HasPrefix(arg, "-") || len(arg) == 1:
		return "", 1, true
	case arg == "--all":
		return "git commit --all", 0, false
	case commitValueLong(arg):
		return "", valueOptionWidth, false
	case strings.HasPrefix(arg, dashDash):
		return "", 1, false
	}

	hasAll, step := commitShort(arg)
	if hasAll {
		return "git commit " + arg, 0, false
	}

	return "", step, false
}

func commitHit(args []string) string {
	words, tail := splitAtDashes(args)
	pos := tail

	for k := 0; k < len(words); {
		hit, step, positional := commitWord(words[k])
		if hit != "" {
			return hit
		}

		if positional {
			pos = append(pos, words[k])
		}

		k += step
	}

	return pathspecHit("git commit", pos)
}

// splitAtDashes returns the words before the first `--` and the words after it.
func splitAtDashes(args []string) ([]string, []string) {
	at := slices.Index(args, dashDash)
	if at < 0 {
		return args, nil
	}

	return args[:at], args[at+1:]
}

// subcommandAt returns the index of the git subcommand, past the global options.
func subcommandAt(head []string) int {
	i := 1
	for i < len(head) && strings.HasPrefix(head[i], "-") {
		i += optionWidth(slices.Contains([]string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path"}, head[i]))
	}

	return i
}

// bulkHit returns the bulk staging a git command does as text, or an empty string.
func bulkHit(head []string) string {
	if len(head) == 0 || path.Base(head[0]) != "git" {
		return ""
	}

	i := subcommandAt(head)
	if i >= len(head) {
		return ""
	}

	switch head[i] {
	case "add", "stage":
		return addHit(head[i], head[i+1:])
	case "commit":
		return commitHit(head[i+1:])
	}

	return ""
}

// hasPrivateIndex reports whether a prefix word sets GIT_INDEX_FILE to a value, so git stages into its own index.
func hasPrivateIndex(prefix []string) bool {
	return slices.ContainsFunc(prefix, func(word string) bool {
		return strings.HasPrefix(word, privateIndex) && len(word) > len(privateIndex)
	})
}
