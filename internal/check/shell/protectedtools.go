package shell

import (
	"slices"
	"strings"
)

const (
	optFile = "--file"
	subAdd  = "add"
)

// downloadTargets returns the paths a tar, curl, wget or unzip command writes. Another command returns nothing.
func downloadTargets(name string, args []string) []string {
	switch name {
	case cmdTar:
		return tarTargets(args)
	case "curl":
		return curlTargets(args)
	case "wget":
		return wgetTargets(args)
	case "unzip":
		return unzipTargets(args)
	}

	return nil
}

// sedFiles returns the words of a sed or perl command that can be files. Without -e or -f the first plain word is the script.
func sedFiles(args []string) []string {
	words := plainWords(args)

	if hasScriptOption(args) || len(words) == 0 {
		return words
	}

	return words[1:]
}

func hasScriptOption(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		return slices.Contains([]string{"-e", "-f", "--expression", optFile}, arg) || strings.HasPrefix(arg, "--expression=") || strings.HasPrefix(arg, optFile+"=")
	})
}

// plainWords returns the words that are not options.
func plainWords(args []string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return strings.HasPrefix(arg, "-") && arg != "-" })
}

// destinations returns the paths a copy writes: the value of -t or --target-directory, or the last plain word.
func destinations(args []string) []string {
	if targets := optionValues(args, "-t", "--target-directory"); len(targets) > 0 {
		return targets
	}

	words := plainWords(args)
	if len(words) == 0 {
		return nil
	}

	return words[len(words)-1:]
}

// optionValues returns the values of an option given as `-x value`, `-xvalue`, `--long value` or `--long=value`. An empty short or long name leaves that form out.
func optionValues(args []string, short, long string) []string {
	var out []string

	for i := range args {
		if value, ok := optionValue(args, i, short, long); ok {
			out = append(out, value)
		}
	}

	return out
}

// optionValue returns the value that the option at index i gives, if the word is the option.
func optionValue(args []string, i int, short, long string) (string, bool) {
	arg := args[i]

	switch {
	case long != "" && strings.HasPrefix(arg, long+"="):
		return strings.TrimPrefix(arg, long+"="), true
	case arg != "" && (arg == short || arg == long):
		return nextWord(args, i)
	case short != "" && len(arg) > len(short) && strings.HasPrefix(arg, short) && !strings.HasPrefix(arg, dashDash):
		return arg[len(short):], true
	}

	return "", false
}

func nextWord(args []string, i int) (string, bool) {
	if i+1 < len(args) {
		return args[i+1], true
	}

	return "", false
}

// clusterValues returns the word after each cluster of short options that ends in the letter, as in `wget -qO file`.
func clusterValues(args []string, letter string) []string {
	var out []string

	for i, arg := range args {
		if len(arg) > len("-x") && isShortCluster(arg) && strings.HasSuffix(arg, letter) {
			if value, ok := nextWord(args, i); ok {
				out = append(out, value)
			}
		}
	}

	return out
}

func isShortCluster(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, dashDash)
}

// notStdout drops the value "-", which is the standard output.
func notStdout(values []string) []string {
	return slices.DeleteFunc(values, func(value string) bool { return value == "-" || value == "" })
}

// tarTargets returns the paths a tar command writes: the directory of an extract (the current directory when `-C` is missing) and the archive of a create.
func tarTargets(args []string) []string {
	extract, create := tarModes(args)

	var out []string

	if extract {
		out = optionValues(args, "-C", "--directory")
		if len(out) == 0 {
			out = []string{"."}
		}
	}

	if create {
		out = append(out, tarArchives(args)...)
	}

	return notStdout(out)
}

// tarArchives returns the archive files that a tar command names by `--file` or by an `f` in a cluster of options.
func tarArchives(args []string) []string {
	out := optionValues(args, "", optFile)

	for i, arg := range args {
		if (isShortCluster(arg) || i == 0 && !strings.HasPrefix(arg, "-")) && strings.Contains(arg, "f") {
			if value, ok := nextWord(args, i); ok {
				out = append(out, value)
			}
		}
	}

	return out
}

// tarModes reports whether the options of tar extract, and whether they create, append or update an archive. The mode may be the first word with no dash, as in `tar xf`.
func tarModes(args []string) (bool, bool) {
	var extract, create bool

	for i, arg := range args {
		x, c := tarWordModes(arg, i)
		extract, create = extract || x, create || c
	}

	return extract, create
}

func tarWordModes(arg string, i int) (bool, bool) {
	switch {
	case arg == "--extract" || arg == "--get":
		return true, false
	case arg == "--create" || arg == "--append" || arg == "--update":
		return false, true
	case strings.HasPrefix(arg, dashDash) || !isShortCluster(arg) && i != 0:
		return false, false
	}

	letters := strings.TrimPrefix(arg, "-")

	return strings.Contains(letters, "x"), strings.ContainsAny(letters, "cru")
}

// curlTargets returns the paths a curl command writes: the value of `-o`, the output directory, and the current directory for `-O`.
func curlTargets(args []string) []string {
	dirs := optionValues(args, "", "--output-dir")
	out := slices.Concat(optionValues(args, "-o", "--output"), clusterValues(args, "o"), dirs)

	remote := slices.ContainsFunc(args, func(arg string) bool {
		return slices.Contains([]string{"-O", "--remote-name", "--remote-name-all"}, arg)
	})
	if remote && len(dirs) == 0 {
		out = append(out, ".")
	}

	return notStdout(out)
}

// wgetTargets returns the paths a wget command writes: the document file, the directory prefix, or the current directory.
func wgetTargets(args []string) []string {
	out := slices.Concat(optionValues(args, "-O", "--output-document"), clusterValues(args, "O"), optionValues(args, "-P", "--directory-prefix"))
	if len(out) == 0 {
		out = []string{"."}
	}

	return notStdout(out)
}

// unzipTargets returns the directory an unzip command extracts to. A listing, a test or a print writes nothing.
func unzipTargets(args []string) []string {
	if slices.ContainsFunc(args, func(arg string) bool { return slices.Contains([]string{"-l", "-p", "-t", "-v", "-Z", "-z"}, arg) }) {
		return nil
	}

	out := slices.Concat(optionValues(args, "-d", ""), clusterValues(args, "d"))
	if len(out) == 0 {
		out = []string{"."}
	}

	return notStdout(out)
}

// gitTargets returns the paths a git subcommand writes besides its repository: the file of `--output`, the clone destination, the path of `worktree add`
// and the directory of `apply --directory`. The sources a command reads are left out.
func gitTargets(sub string, args []string) []string {
	return append(optionValues(args, "", "--output"), gitSubTargets(sub, args)...)
}

func gitSubTargets(sub string, args []string) []string {
	switch sub {
	case "bundle":
		return bundleTargets(positionals(args))
	case "clone":
		return cloneTargets(args)
	case "init":
		return initTargets(args)
	case "worktree":
		return worktreeTargets(positionals(args, "-b", "-B", "--reason"))
	}

	return gitFileOptions(sub, args)
}

// gitFileOptions returns the values of the options that name the file or the directory a subcommand writes.
func gitFileOptions(sub string, args []string) []string {
	switch sub {
	case "archive":
		return optionValues(args, "-o", "")
	case "format-patch":
		return optionValues(args, "-o", "--output-directory")
	case "apply":
		return optionValues(args, "", "--directory")
	}

	return nil
}

func bundleTargets(words []string) []string {
	if len(words) > 1 && words[0] == "create" {
		return words[1:2:2]
	}

	return nil
}

// cloneTargets returns the destination of git clone and its separate git directory. The first word is the source, which the command reads.
func cloneTargets(args []string) []string {
	out := optionValues(args, "", "--separate-git-dir")
	words := positionals(args, "-b", "--branch", "--depth", "-o", "--origin", "--reference", "--reference-if-able", "-c", "--config", "--template", "--separate-git-dir",
		"-j", "--jobs", "--filter", "--shallow-since", "--shallow-exclude", "--upload-pack", "-u", "--server-option", "--bundle-uri", "--ref-format")

	if len(words) > 1 {
		out = append(out, words[1])
	}

	return out
}

func initTargets(args []string) []string {
	out := optionValues(args, "", "--separate-git-dir")

	if words := positionals(args, "--template", "--initial-branch", "-b", "--object-format"); len(words) > 0 {
		out = append(out, words[0])
	}

	return out
}

// worktreeTargets returns the paths a `git worktree` command writes. The commit of `add` is read.
func worktreeTargets(words []string) []string {
	if len(words) == 0 {
		return nil
	}

	if rest := words[1:]; words[0] == subAdd && len(rest) > 0 {
		return rest[:1]
	}

	return words[1:]
}

// positionals returns the words that are not options. An option in valued takes the next word as its value.
func positionals(args []string, valued ...string) []string {
	var out []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == dashDash:
			return append(out, args[i+1:]...)
		case strings.HasPrefix(args[i], "-") && args[i] != "-":
			if slices.Contains(valued, args[i]) {
				i++
			}
		default:
			out = append(out, args[i])
		}
	}

	return out
}
