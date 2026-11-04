package shell

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	cmdCd         = "cd"
	cmdTar        = "tar"
	cannotResolve = " (a path cannot be resolved)"
)

var (
	// destinationWriter matches the commands that write only the last path they name, or the path of `-t`. The source of a copy is read.
	destinationWriter = regexp.MustCompile(`^(cp|install|ln|rsync)$`)
	// pathWriter matches the commands that write every path they name.
	pathWriter = regexp.MustCompile(`^(tee|mv|touch|truncate|mkdir|rmdir|rm|unlink|chmod|chown|chgrp|patch|shred)$`)
	// gitReader matches the git subcommands that only read. Any other subcommand, an alias included, writes as far as this kind can tell.
	// The commands that write a file by an option (archive, format-patch) read the repository: gitTargets judges the file.
	gitReader = regexp.MustCompile(`^(log|show|diff|status|ls-files|ls-tree|rev-parse|rev-list|cat-file|blame|grep|describe|show-ref|merge-base|for-each-ref|` +
		`name-rev|shortlog|diff-tree|diff-files|diff-index|ls-remote|check-ignore|count-objects|version|help|whatchanged|range-diff|cherry|verify-commit|verify-tag|var|show-branch|` +
		`archive|format-patch)$`)
	// gitDirOption matches the global git options that move the repository or the work tree, with an optional `=value`.
	gitDirOption = regexp.MustCompile(`^--(git-dir|work-tree)(=(.*))?$`)
)

// scanProtected adds a finding when the command writes in a protected directory. It also follows `cd`, so a relative path means the right place.
func (s *scanner) scanProtected(cmd *command) {
	if s.prot == nil {
		return
	}

	s.protectRedirects(cmd.writes)

	if len(cmd.head) == 0 {
		return
	}

	defer s.enterEnvDir(cmd.prefix)()

	s.blind = false
	s.protectCommand(path.Base(cmd.head[0]), cmd)
}

// protectCommand judges the command word of a command: a directory change, a git command or another writer.
func (s *scanner) protectCommand(name string, cmd *command) {
	switch {
	case name == cmdCd || name == "pushd":
		s.changeDir(cmd.head)
	case name == cmdGit:
		s.protectGit(cmd)
	case s.writesProtected(name, cmd.head[1:]):
		s.refuse(cmd.head)
	}
}

// refuse adds a finding for a command. The text says when a path of the command could not be resolved.
func (s *scanner) refuse(head []string) {
	text := strings.Join(head, " ")
	if s.blind {
		text += cannotResolve
	}

	s.findings = append(s.findings, model.Finding{Kind: model.FindingProtected, Text: text})
}

// enterEnvDir moves the directory of the scan to the one that `env -C dir` or `env --chdir dir` gives, and returns the function that moves it back.
// A redirect is not affected: the shell opens it before env runs.
func (s *scanner) enterEnvDir(prefix []string) func() {
	dir, ok := envDir(prefix)
	if !ok {
		return func() {}
	}

	saved := s.cwd
	s.cwd, _ = s.whereFrom(saved, dir)

	return func() { s.cwd = saved }
}

// envDir returns the directory of `env -C dir` or `env --chdir dir` among the words before a command.
func envDir(prefix []string) (string, bool) {
	if !slices.ContainsFunc(prefix, func(word string) bool { return path.Base(word) == cmdEnv }) {
		return "", false
	}

	for i, word := range prefix {
		switch {
		case word == "-C" || word == "--chdir":
			return nextWord(prefix, i)
		case strings.HasPrefix(word, "--chdir="):
			return strings.TrimPrefix(word, "--chdir="), true
		}
	}

	return "", false
}

// protectRedirects adds a finding for each redirect target that is in a protected directory.
func (s *scanner) protectRedirects(targets []string) {
	for _, target := range targets {
		s.blind = false

		if s.touches(target) {
			s.refuse([]string{">", target})
		}
	}
}

// changeDir follows `cd dir`. The new directory is worked out from the old one before it replaces it.
// A target that no static read can tell leaves the directory unknown, and is a finding: the later commands cannot be placed.
func (s *scanner) changeDir(head []string) {
	args := slices.DeleteFunc(slices.Clone(head[1:]), func(arg string) bool {
		return arg == "-P" || arg == "-L" || arg == "-e" || arg == "-@" || arg == dashDash
	})

	next := ""

	if len(args) == 1 && args[0] != "-" {
		target := s.expandPlain(args[0])

		if unsettled(target) {
			s.blind = true
			s.refuse(head)
		}

		next, _ = s.whereFrom(s.cwd, target)
	}

	s.cwd = next
}

// writesProtected reports whether a command other than git writes a path in a protected directory.
func (s *scanner) writesProtected(name string, args []string) bool {
	switch {
	case destinationWriter.MatchString(name):
		return slices.ContainsFunc(destinations(args), s.touches)
	case pathWriter.MatchString(name):
		return slices.ContainsFunc(plainWords(args), s.touches)
	}

	return s.writesByOption(name, args)
}

// writesByOption reports whether a command that writes by an option or a flag writes a protected path: sed -i, dd of=, find -delete, tar, curl, wget and unzip.
func (s *scanner) writesByOption(name string, args []string) bool {
	switch name {
	case "sed", "perl":
		return slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "-i") || arg == "--in-place" }) && slices.ContainsFunc(sedFiles(args), s.touches)
	case "dd":
		return slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "of=") && s.touches(arg[len("of="):]) })
	case cmdFind:
		return deleteHit(append([]string{name}, args...)) && slices.ContainsFunc(append(findPaths(args), "."), s.touches)
	}

	return slices.ContainsFunc(downloadTargets(name, args), s.touches)
}

// protectGit adds a finding for a git command that works in a protected directory or writes a file there.
// A command writes in the directory when the directory is its repository (`-C`, `--git-dir`, `--work-tree`, GIT_DIR, GIT_WORK_TREE or the current directory)
// and the command is not a read, or when a path it writes is there (the clone destination, the path of `worktree add`, the file of `--output`).
// A path that the command only reads, such as the source of a clone or the address of a remote, does not count.
func (s *scanner) protectGit(cmd *command) {
	head := cmd.head
	at := subcommandAt(head)

	if at >= len(head) {
		return
	}

	options, args := head[1:at], head[at+1:]
	cwd := s.gitCwd(options)
	hit := !s.gitReads(head[at], readFlags(args)) && s.gitPlaces(cwd, options, cmd.prefix)

	if hit || slices.ContainsFunc(gitTargets(head[at], args), func(target string) bool { return s.touchesFrom(cwd, target) }) {
		s.refuse(head[:at+1])
	}
}

// gitReads reports whether a git subcommand only reads the repository.
func (s *scanner) gitReads(sub string, set flagSet) bool {
	switch sub {
	case subBranch, "tag", cmdConfig, "remote", "notes", subStash, "submodule":
		return !changesRepository(sub, set)
	case "worktree":
		return firstIs(set, "list")
	case "bundle":
		return firstIs(set, "create", "verify", "list-heads")
	}

	return gitReader.MatchString(sub)
}

// gitCwd returns the directory a git command works in after its `-C` options, or "" when no static read can tell.
func (s *scanner) gitCwd(options []string) string {
	cwd := s.cwd
	moves, _ := gitOptionPlaces(options)

	for _, move := range moves {
		cwd, _ = s.whereFrom(cwd, move)
	}

	return cwd
}

// gitPlaces reports whether a place a git command works in is protected, or cannot be read.
// The places are the directory of the last `-C`, the repository and the work tree.
func (s *scanner) gitPlaces(cwd string, options, prefix []string) bool {
	_, places := gitOptionPlaces(options)
	places = append(places, gitEnvPlaces(prefix)...)

	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		if value, ok := s.plain[name]; ok {
			places = append(places, value)
		}
	}

	if cwd == "" {
		s.blind = true

		return true
	}

	return s.prot.holds(cwd) || slices.ContainsFunc(places, func(place string) bool { return s.touchesFrom(cwd, place) })
}

// gitOptionPlaces returns the values of the `-C` options, in order, and the values of `--git-dir` and `--work-tree`.
func gitOptionPlaces(options []string) ([]string, []string) {
	var moves, places []string

	for i := 0; i < len(options); i++ {
		match := gitDirOption.FindStringSubmatch(options[i])

		switch {
		case options[i] == "-C" && i+1 < len(options):
			i++
			moves = append(moves, options[i])
		case match != nil && match[2] != "":
			places = append(places, match[3])
		case match != nil && i+1 < len(options):
			i++
			places = append(places, options[i])
		}
	}

	return moves, places
}

// gitEnvPlaces returns the values of GIT_DIR and GIT_WORK_TREE among the assignments before a git command.
func gitEnvPlaces(prefix []string) []string {
	var places []string

	for _, word := range prefix {
		if name, value, found := strings.Cut(word, "="); found && (name == "GIT_DIR" || name == "GIT_WORK_TREE") {
			places = append(places, value)
		}
	}

	return places
}
