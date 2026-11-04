package shell

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

var (
	// remoteDeleteKey names the remote settings that make a plain push delete or mirror branches with no flag.
	remoteDeleteKey = regexp.MustCompile(`(?i)^remote\..+\.(mirror|push)$`)
	// gitRefPath matches a forge API path of the refs of a repository, such as repos/o/r/git/refs/heads/b.
	gitRefPath  = regexp.MustCompile(`(^|/)git/refs(/|$)`)
	authorKey   = regexp.MustCompile(`(?i)^(user|author|committer)\.(name|email)$`)
	authorEnv   = regexp.MustCompile(`^GIT_(AUTHOR|COMMITTER)_(NAME|EMAIL|DATE)$`)
	overrideEnv = regexp.MustCompile(`^(PATH|BASH_ENV|ENV|ZDOTDIR|PROMPT_COMMAND|NODE_OPTIONS|GIT_EXEC_PATH|GIT_CONFIG_GLOBAL|GIT_CONFIG_SYSTEM|GIT_CONFIG_COUNT|LD_[A-Z_]+|DYLD_[A-Z_]+)$`)
)

const (
	cmdGit      = "git"
	cmdGh       = "gh"
	subSendPack = "send-pack"
	cmdConfig   = "config"
	subCommit   = "commit"
	noVerify    = "--no-verify"
	forceOption = "--force"
	configEnv   = "--config-env"
)

var (
	// startupFile matches the files a shell or a tool reads at start. A lane that writes one changes every later command.
	startupFile = regexp.MustCompile(`^\.(bashrc|bash_profile|bash_login|profile|bash_logout|zshrc|zshenv|zprofile|zlogin|zlogout|envrc)$`)
	// secretFile matches the files that hold a key. A lane reads a named key from them and never prints the whole file.
	secretFile = regexp.MustCompile(`^(\.netrc|\.env|\.env\..+|\.npmrc|credentials|\.git-credentials|\.pgpass|hosts\.yml|id_rsa|id_ed25519|\.boto|auth\.json|.+\.pem|.+\.key)$`)
	// fileWriter matches the commands whose words name a file they write.
	fileWriter = regexp.MustCompile(`^(tee|cp|mv|ln|install|touch|truncate|sed)$`)
	// fileReader matches the commands that print a whole file.
	fileReader = regexp.MustCompile(`^(cat|less|more|head|tail|bat|tac|nl|od|xxd|strings|view)$`)
	// listsVariables matches the declaration commands that print every variable when they name none.
	listsVariables = regexp.MustCompile(`^(export|declare|typeset|readonly)$`)
	// processEnviron matches the file of a process that holds its environment.
	processEnviron = regexp.MustCompile(`^/proc/[^/]+/environ$`)
)

// scanRules adds the findings of the rules that need no more than the words of one command.
func (s *scanner) scanRules(cmd *command, remote bool) {
	s.scanOpaque(cmd)
	s.scanPolicy(cmd)
	s.scanProtected(cmd)
	s.scanLaptopCopy(cmd, remote)
}

// redirects returns the heredoc bodies of a statement and the targets of its redirects that write.
func (s *scanner) redirects(u Unit, stmt *syntax.Stmt) ([]string, []string) {
	var bodies, targets []string

	for _, redir := range stmt.Redirs {
		if (redir.Op == syntax.Hdoc || redir.Op == syntax.DashHdoc) && redir.Hdoc != nil {
			bodies = append(bodies, SrcOf(u.Src, redir.Hdoc))
		}

		if redir.Word != nil && slices.Contains([]syntax.RedirOperator{syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob, syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob}, redir.Op) {
			targets = append(targets, s.literal(u, redir.Word))
		}
	}

	return bodies, targets
}

// inputFiles returns the files that the redirects of a statement read as standard input. A heredoc and a here-string are text of the command, not a file.
func (s *scanner) inputFiles(u Unit, stmt *syntax.Stmt) []string {
	var files []string

	for _, redir := range stmt.Redirs {
		if redir.Word != nil && (redir.Op == syntax.RdrIn || redir.Op == syntax.RdrInOut) {
			files = append(files, s.literal(u, redir.Word))
		}
	}

	return files
}

// scanPolicy adds the findings of the rules about the author, the git safeguards, the shell start and the files with a key.
func (s *scanner) scanPolicy(cmd *command) {
	for _, word := range cmd.prefix {
		s.scanAssignment(word)
	}

	for _, target := range cmd.writes {
		s.scanWrite(target)
	}

	if len(cmd.head) == 0 {
		if slices.ContainsFunc(cmd.prefix, func(word string) bool { return path.Base(word) == cmdEnv }) {
			s.add(model.FindingConfigRead, []string{cmdEnv})
		}

		return
	}

	s.scanGit(cmd.head)
	s.scanForgeAPI(cmd.head)
	s.scanFiles(cmd.head)
}

// scanForgeAPI judges `gh api`, which changes a branch or a tag through the forge with no git command:
// the method DELETE on a git ref path is a remote-delete; the method PATCH on a git ref path with force=true is a force-push;
// a GraphQL query that names deleteRef is a remote-delete, and one that names updateRef is a force-push.
// The query read from a file (`-F query=@file`) and a method that a variable gives are not read.
func (s *scanner) scanForgeAPI(head []string) {
	if path.Base(head[0]) != cmdGh || !slices.Contains(head[1:], "api") {
		return
	}

	onRef := slices.ContainsFunc(head[1:], gitRefPath.MatchString)
	method := func(want string) bool {
		return slices.ContainsFunc(methodsOf(head), func(m string) bool { return strings.EqualFold(m, want) })
	}
	names := func(text string) bool {
		return slices.ContainsFunc(head[1:], func(word string) bool { return strings.Contains(word, text) })
	}

	switch {
	case onRef && method("DELETE"), names("deleteRef"):
		s.add(model.FindingRemoteDelete, head)
	case onRef && method("PATCH") && names("force=true"), names("updateRef"):
		s.add(model.FindingGitBypass, head)
	}
}

// methodsOf lists the HTTP methods that a `gh api` command names: `--method M`, `--method=M`, `-X M` and `-XM`.
func methodsOf(head []string) []string {
	var methods []string

	for i, word := range head {
		if method, ok := strings.CutPrefix(word, "--method="); ok {
			methods = append(methods, method)
		} else if (word == "--method" || word == "-X") && i+1 < len(head) {
			methods = append(methods, head[i+1])
		} else if strings.HasPrefix(word, "-X") && !strings.HasPrefix(word, dashDash) {
			methods = append(methods, word[len("-X"):])
		}
	}

	return methods
}

// declared reads the assignments of `export`, `declare` and the like.
func (s *scanner) declared(u Unit, clause *syntax.DeclClause) {
	named := false

	for _, assign := range clause.Args {
		if assign.Name != nil {
			named = true
		}

		if assign.Name != nil && assign.Value != nil {
			s.scanAssignment(assign.Name.Value + "=" + s.literal(u, assign.Value))
		}
	}

	// A declaration with no name prints every variable of the shell.
	if !named && clause.Variant != nil && listsVariables.MatchString(clause.Variant.Value) {
		s.add(model.FindingConfigRead, []string{clause.Variant.Value})
	}
}

// bare reads the assignments of a command with no command word: they change the shell that runs every later command.
func (s *scanner) bare(u Unit, call *syntax.CallExpr) {
	for _, word := range s.assigned(u, call) {
		s.scanAssignment(word)
	}
}

// scanAssignment judges one `NAME=value` word.
func (s *scanner) scanAssignment(word string) {
	name, _, found := strings.Cut(word, "=")

	switch {
	case !found:
	case authorEnv.MatchString(name):
		s.add(model.FindingGitAuthor, []string{name})
	case overrideEnv.MatchString(name):
		s.add(model.FindingEnvOverride, []string{name})
	}
}

// scanWrite judges the target of a redirect that writes.
func (s *scanner) scanWrite(target string) {
	if startupFile.MatchString(path.Base(target)) {
		s.add(model.FindingEnvOverride, []string{">", target})
	}
}

// scanFiles judges a command that writes a start file or prints a file with a key.
func (s *scanner) scanFiles(head []string) {
	name := path.Base(head[0])

	if slices.ContainsFunc(head[1:], processEnviron.MatchString) {
		s.add(model.FindingConfigRead, head)
	}

	switch {
	case name == "set" && len(head) == 1:
		s.add(model.FindingConfigRead, head)
	case fileWriter.MatchString(name):
		if slices.ContainsFunc(head[1:], func(word string) bool { return startupFile.MatchString(path.Base(word)) }) {
			s.add(model.FindingEnvOverride, head)
		}
	case fileReader.MatchString(name):
		if slices.ContainsFunc(head[1:], hasKey) {
			s.add(model.FindingConfigRead, head)
		}
	case name == "printenv" && len(head) == 1:
		s.add(model.FindingConfigRead, head)
	}
}

// hasKey reports whether the word names a file that holds a key.
func hasKey(word string) bool {
	base := path.Base(word)

	return secretFile.MatchString(base)
}

// scanGit judges a git command: the author, the safeguards and the config.
func (s *scanner) scanGit(head []string) {
	if path.Base(head[0]) != cmdGit {
		return
	}

	at := subcommandAt(head)
	s.scanGlobals(head[1:min(at, len(head))])

	if at >= len(head) {
		return
	}

	s.scanSubcommand(head[:at+1], readFlags(head[at+1:]))
}

// scanSubcommand judges one git subcommand. named ends with the subcommand.
func (s *scanner) scanSubcommand(named []string, set flagSet) {
	sub := named[len(named)-1]

	if sub == cmdConfig {
		s.scanConfig(named, set)
	}

	if sub == subCommit && (hasLong(set, "--author") || hasLong(set, "--reset-author")) {
		s.add(model.FindingGitAuthor, named)
	}

	if s.readOnly && changesRepository(sub, set) {
		s.add(model.FindingGitWrite, named)
	}

	if skipsSafeguard(sub, set) {
		s.add(model.FindingGitBypass, named)
	}
}

// skipsSafeguard reports whether a git command skips the hooks, hides work in a stash, or overwrites or deletes a remote branch.
func skipsSafeguard(sub string, set flagSet) bool {
	switch sub {
	case subStash:
		return len(set.pos) == 0 || !slices.Contains([]string{"list", "show"}, set.pos[0])
	case subCommit:
		return set.has("n", noVerify) || hasLong(set, noVerify)
	case "push", subSendPack:
		return pushForces(set) || pushDeletes(set) || hasLong(set, noVerify)
	case "merge", "rebase", "am", "cherry-pick":
		return hasLong(set, noVerify)
	}

	return false
}

// scanGlobals judges the `-c name=value` options before the git subcommand.
func (s *scanner) scanGlobals(options []string) {
	for i, option := range options {
		setting := configSetting(options, i)
		if setting == "" {
			continue
		}

		key, _, _ := strings.Cut(setting, "=")

		switch {
		case authorKey.MatchString(key):
			s.add(model.FindingGitAuthor, []string{cmdGit, option, setting})
		case strings.EqualFold(key, "core.hooksPath"), remoteDeleteKey.MatchString(key):
			s.add(model.FindingGitBypass, []string{cmdGit, option, setting})
		}
	}
}

// configSetting returns the `name=value` setting that the option at i gives: `-c name=value`, `-cname=value`, `--config-env name=ENV` or `--config-env=name=ENV`. It returns an empty string for any other option.
func configSetting(options []string, i int) string {
	option := options[i]

	switch {
	case option == "-c" || option == configEnv:
		if i+1 < len(options) {
			return options[i+1]
		}
	case strings.HasPrefix(option, configEnv+"="):
		return strings.TrimPrefix(option, configEnv+"=")
	case strings.HasPrefix(option, "-c") && !strings.HasPrefix(option, dashDash):
		return option[len("-c"):]
	}

	return ""
}

// scanConfig judges `git config`: a write is refused, a read of one key is not, and a list prints every key.
func (s *scanner) scanConfig(named []string, set flagSet) {
	if listsConfig(set) {
		s.add(model.FindingConfigRead, named)
	}

	if len(set.pos) > 1 && authorKey.MatchString(set.pos[0]) {
		s.add(model.FindingGitAuthor, named)
	}

	reads := set.has("l", "--list") || slices.ContainsFunc(set.longs, func(given string) bool { return strings.HasPrefix(given, "--get") })
	if reads && !slices.ContainsFunc(set.longs, configWriteOption) {
		return
	}

	if len(set.pos) > 1 || set.has("e", "--edit") || slices.ContainsFunc(set.longs, configWriteOption) {
		s.add(model.FindingGitBypass, named)
	}
}

// listsConfig reports whether `git config` prints every key: `--list`, `-l` or the `list` subcommand.
func listsConfig(set flagSet) bool {
	return set.has("l", "--list") || (len(set.pos) == 1 && set.pos[0] == "list")
}

func configWriteOption(given string) bool {
	name, _, _ := strings.Cut(given, "=")

	return slices.Contains([]string{"--add", "--replace-all", "--rename-section", "--remove-section"}, name) || strings.HasPrefix(name, "--unset")
}

// pushForces reports whether a push overwrites a remote branch: a force option, or a refspec that starts with `+`.
func pushForces(set flagSet) bool {
	if set.has("f", forceOption) || hasLong(set, forceOption) || hasLong(set, "--force-with-lease") || hasLong(set, "--force-if-includes") {
		return true
	}

	return slices.ContainsFunc(set.pos, func(word string) bool { return strings.HasPrefix(word, "+") })
}

// pushDeletes reports whether a push removes a remote branch: `--delete`, `-d`, a refspec with no source (`:b`), or `--prune` and `--mirror`, which remove each remote branch that the local side lacks.
func pushDeletes(set flagSet) bool {
	if set.has("d", "--delete") || hasLong(set, "--delete") || hasLong(set, "--prune") || hasLong(set, "--mirror") {
		return true
	}

	return slices.ContainsFunc(set.pos, func(word string) bool { return strings.HasPrefix(word, ":") })
}

// hasLong reports whether a long option is the given one or a unique start of it, as git accepts, with or without `=value`.
func hasLong(set flagSet, long string) bool {
	return slices.ContainsFunc(set.longs, func(given string) bool {
		name, _, _ := strings.Cut(given, "=")

		return len(name) > len(dashDash) && strings.HasPrefix(long, name)
	})
}
