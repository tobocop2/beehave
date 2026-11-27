package main

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/check/shell"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// sandboxPlan makes the plan for the sandbox of one run, or returns the reason why the step cannot start with it.
// The shell may read the work folder, the shared git directories of a linked work tree, the read paths of the project, the data file and the output file of the step, and nothing else under the home folder.
// Outside the work folder, the agent may write only the output file of a step that declares outputs. The run spec carries that file, so every launch opens it, and the adapter adds it to the sandbox.
// The folder of the data file is a working directory.
// The credential paths come from the engine list, the project file and the config folder of the runner. The .env files of the work folder, at any depth
// and whether or not they exist yet, are denied by a pattern that the CLI matches when the file is used. The commits carry the git identity of the engine at the work folder.
func (a agents) sandboxPlan(ctx context.Context, request flow.AgentRequest, folder, dir string) (agent.SandboxPlan, string) {
	home, err := a.home()
	if err != nil {
		return agent.SandboxPlan{}, "beehest cannot find the home folder, so the sandbox cannot deny it. The step did not start."
	}

	project := a.projects[request.Project]
	deny, read := withSocket(credentialPaths(home, folder, project.SandboxDeny), a.env[model.SSHAgentVar]), expandAll(project.SandboxRead, home)
	git := gitOf(a.projects, request.Project, a.repos).Runner(dir, proc.DefaultTimeout)
	closed, err := closedPaths(ctx, git, dir, home)
	identity := a.identity(ctx, git)

	if note := cmp.Or(refusedNote(err), readNote(read, home, deny), stateNote(deny, request.DataFile), workNote(dir, request.NoEdits), identityNote(identity, dir)); note != "" {
		return agent.SandboxPlan{}, note
	}

	return agent.SandboxPlan{
		Work: dir, GitDirs: sharedGitDirs(a.repos, dir), Read: withData(read, request.DataFile), ReadDirs: dataFolder(request.DataFile),
		Deny: deny, DenyPatterns: envPatterns(dir), DenyWrite: writeDenies(dir, closed, request.DataFile), DenyWritePatterns: hookPatterns(dir), Network: request.Sandbox.Network, Identity: identity,
	}, ""
}

// skillFile is the file that every skill folder holds.
const skillFile = "SKILL.md"

// skillFolders returns the folder of each skill that the prompt of the step declares, in the skills folder of the config folder of the runner, or why the step cannot start:
// the runner names no config folder, a folder holds no SKILL.md, or a folder holds a link.
func skillFolders(names []string, configDir, home string) ([]string, string) {
	folders := make([]string, 0, len(names))

	for _, name := range names {
		if configDir == "" {
			return nil, fmt.Sprintf("the step names the skill %s, and its runner names no config folder to find the skill in. The step did not start. Set the config folder of the runner.", name)
		}

		folder := filepath.Join(expand(configDir, home), "skills", name)
		if !isFile(filepath.Join(folder, skillFile)) {
			return nil, fmt.Sprintf("the step names the skill %s, and %s holds no %s. The step did not start. Install the skill there, or remove it from the prompt.", name, folder, skillFile)
		}

		if link := linkIn(folder); link != "" {
			return nil, fmt.Sprintf("the step names the skill %s, and its folder holds the link %s. A copy of the skill would keep the link, and its target can be outside the skill. The step did not start. Replace the link with the file it points to.", name, link)
		}

		folders = append(folders, folder)
	}

	return folders, ""
}

// linkIn returns the first symbolic link inside a skill folder, or "" for none. The folder itself may be a link: the copy reads through it.
// A folder that cannot be read is left to the copy: it reads the same tree, and its error stops the launch.
func linkIn(folder string) string {
	found := ""

	_ = fs.WalkDir(os.DirFS(folder), ".", func(name string, entry fs.DirEntry, _ error) error { //nolint:errcheck // an unreadable folder fails the copy instead
		if entry != nil && entry.Type()&fs.ModeSymlink != 0 {
			found = filepath.Join(folder, name)

			return fs.SkipAll
		}

		return nil
	})

	return found
}

// outputNote says why the output file of a step cannot be opened to the file tools, or returns "" when it can. Every launch opens that file by a rule, so the
// glob check runs for every step, with or without a sandbox. The credential check runs for a sandboxed step only: there the rule would open a path the
// sandbox denies. A step with no sandbox can already read every path, and its rule names the one file. Without a home folder the credential paths
// are not known, and only the glob check runs; a sandboxed step then stops in the plan for want of the home folder.
func (a agents) outputNote(request flow.AgentRequest, folder string) string {
	write := outputWrites(request)
	if len(write) == 0 {
		return ""
	}

	var deny []string

	if home, err := a.home(); err == nil && request.Sandbox != nil {
		deny = credentialPaths(home, folder, a.projects[request.Project].SandboxDeny)
	}

	return stateNote(deny, write...)
}

// outputWrites returns the output file of a step that declares outputs, the one path of the state folder that the agent writes, or none.
func outputWrites(request flow.AgentRequest) []string {
	if len(request.Outputs) == 0 {
		return nil
	}

	return []string{request.OutputFile}
}

// gitIdentity returns the user.name and the user.email that the git of the engine reads in the repository of git.
func gitIdentity(ctx context.Context, git gitexec.Runner) agent.GitIdentity {
	return agent.GitIdentity{
		Name:  strings.TrimSpace(git.ReadConfig(ctx, "user.name").Stdout),
		Email: strings.TrimSpace(git.ReadConfig(ctx, "user.email").Stdout),
	}
}

// refusedNote says why the closed paths of the work folder cannot be read, or returns "" when they can.
func refusedNote(err error) string {
	if err == nil {
		return ""
	}

	return err.Error() + " The step did not start."
}

// identityNote says why the commits of a sandboxed step would have no author, or returns "".
func identityNote(identity agent.GitIdentity, dir string) string {
	if identity.Name != "" && identity.Email != "" {
		return ""
	}

	return fmt.Sprintf("git has no user.name or no user.email at %s, so a commit in the sandbox has no author. The step did not start. "+
		"Set them with git config user.email and git config user.name.", dir)
}

// writeDenies returns the closed paths that the adapter denies for write: the closed paths, less the settings folder of
// the agent CLI, which the CLI writes itself, and less a .git that is a file (a linked work tree), which holds only a pointer.
// A .git that does not exist yet stays in the list, so a step that runs git init finds it closed. The folder of the data file joins the list, so it is read only.
func writeDenies(dir string, closed []string, dataFile string) []string {
	out := make([]string, 0, len(closed))

	for _, path := range closed {
		switch {
		case path == filepath.Join(dir, ".claude"):
		case path == filepath.Join(dir, ".git") && isFile(path):
		default:
			out = append(out, path)
		}
	}

	return append(out, dataFolder(dataFile)...)
}

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

// patternsPerName is the pattern of a name and the pattern of its contents.
const patternsPerName = 2

// patternDepths is the top level and every depth: the docs of the CLI give no rule for a ** that matches no folder, so the top level is a pattern of its own.
const patternDepths = 2

// hookPatterns returns the patterns that deny write to the places where a hook or an agent setting would run later, outside the sandbox, as the owner:
// the hooks of a git repository, the .husky and .githooks folders, and the settings files and hooks of the agent CLI, at the top of the work folder
// and at every depth. The CLI matches a pattern when a path is used, so a repository that a step makes later is covered. The rest of .claude stays
// writable, because the CLI keeps its own state there.
func hookPatterns(dir string) []string {
	names := []string{".git/hooks", ".husky", ".githooks", ".claude/hooks", ".claude/settings*.json"}
	out := make([]string, 0, len(names)*patternsPerName*patternDepths)

	for _, name := range names {
		for _, full := range []string{filepath.Join(dir, name), filepath.Join(dir, "**", name)} {
			out = append(out, full, full+"/**")
		}
	}

	return out
}

// envPatterns returns the patterns of the .env files of the work folder: the top level and every depth.
func envPatterns(dir string) []string {
	return []string{filepath.Join(dir, model.EnvPrefix+"*"), filepath.Join(dir, "**", model.EnvPrefix+"*")}
}

// closedPaths returns the paths under the work folder that a step may never write: the closed names and the hooks folder that core.hooksPath names.
func closedPaths(ctx context.Context, git gitexec.Runner, dir, home string) ([]string, error) {
	paths := make([]string, 0, len(model.ClosedNames())+1)

	for _, name := range model.ClosedNames() {
		paths = append(paths, filepath.Join(dir, name))
	}

	hooks, err := hooksPath(ctx, git, dir, home)
	if err != nil {
		return nil, err
	}

	if hooks != "" {
		paths = append(paths, hooks)
	}

	return paths, nil
}

// hooksPath returns the hooks folder that core.hooksPath names for the repository of git, as an absolute path against dir, or "" when it is not set.
// A clone whose git config cannot be read, or holds a setting outside the allow list, is an error: its hooks folder cannot be read, so no plan can deny it.
func hooksPath(ctx context.Context, git gitexec.Runner, dir, home string) (string, error) {
	result := git.ReadConfig(ctx, "core.hooksPath")

	if result.SetupErr != nil {
		return "", result.SetupErr
	}

	text := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 || text == "" {
		return "", nil
	}

	return filepath.Clean(absolute(expand(text, home), dir)), nil
}

// readNote says why a sandbox_read path cannot start the step, or returns "". A path that is the home folder, holds it, or overlaps a credential path
// would put the credentials back in the read base; a .env file is refused for the same reason.
func readNote(read []string, home string, deny []string) string {
	for _, path := range read {
		switch {
		case within(path, home):
			return fmt.Sprintf("The project lists the sandbox_read path %s, which is or holds the home folder. The step did not start. Remove the path.", path)
		case overlap(path, deny) != "":
			return fmt.Sprintf("The project lists the sandbox_read path %s, which overlaps the credential path %s. The step did not start. Remove the path.", path, overlap(path, deny))
		case model.EnvPart(path) != "":
			return fmt.Sprintf("The project lists the sandbox_read path %s, which reaches %s. The step did not start. Remove the path.", path, model.EnvPart(path))
		}
	}

	return ""
}

// withData returns the read paths with the data file of the step, the one path of the state folder that the shell reads. It joins after the check
// of the project paths, which refuses every path under the home folder. A step with no data file keeps the paths as they are.
func withData(read []string, file string) []string {
	if file == "" {
		return read
	}

	return append(read, file)
}

// dataFolder returns the folder of the data file as the one working directory that the step adds, or none for a step with no data file. The folder holds that
// file alone, so the CLI blocks the read of every other file of the state folder, and the plan denies the write of the folder.
func dataFolder(file string) []string {
	if file == "" {
		return nil
	}

	return []string{filepath.Dir(file)}
}

// globCharacters are the characters that make a path of a sandbox rule a pattern.
const globCharacters = `*?[\`

// stateNote says why a file of the state folder that the sandbox opens, the data file or the output file, cannot be opened, or returns "".
// Such a file in a credential path would open that path. The CLI reads its rules as patterns and does not escape them, so a file whose path holds a
// glob character would open more than that file.
func stateNote(deny []string, files ...string) string {
	for _, file := range files {
		if note := globNote("The file "+file+" of the state folder", file, "Move the state folder of beehest"); note != "" {
			return note
		}

		if secret := overlap(file, deny); file != "" && secret != "" {
			return fmt.Sprintf("The file %s of the state folder overlaps the credential path %s. The step did not start. Move the state folder of beehest out of the credential path.", file, secret)
		}
	}

	return ""
}

// globNote says why a path that a write rule of the sandbox names cannot hold a glob character, or returns "" when it holds none.
// subject names the path in the text, and move names what to move.
func globNote(subject, path, move string) string {
	at := strings.IndexAny(path, globCharacters)
	if at < 0 {
		return ""
	}

	return fmt.Sprintf("%s holds a glob character, %s (one of %s), so the sandbox would read its path as a pattern. The step did not start. %s to a path with no glob character.",
		subject, path[at:at+1], globCharacters, move)
}

// workNote says why the work folder of a step that may edit cannot get its write rule, or returns "" when it can.
func workNote(dir string, noEdits bool) string {
	if noEdits {
		return ""
	}

	return globNote("The work folder "+dir, dir, "Move the work folder")
}

// markClosed marks each refused edit of a run whose target a deny rule of the sandbox plan covers. A run with no plan has no deny rule.
func markClosed(denials []agent.Denial, box *agent.SandboxPlan) []agent.Denial {
	if box == nil {
		return denials
	}

	for i := range denials {
		denials[i].Closed = denials[i].Edit && deniesWrite(*box, denials[i].Target)
	}

	return denials
}

// deniesWrite says whether a deny rule of the plan covers a write of path: a credential path or a closed path, or a pattern of either.
func deniesWrite(plan agent.SandboxPlan, path string) bool {
	return slices.ContainsFunc(slices.Concat(plan.Deny, plan.DenyWrite, plan.DenyPatterns, plan.DenyWritePatterns), func(rule string) bool { return within(rule, path) })
}

// credentialPaths returns the absolute credential paths: the engine list expanded under home, the config folder of the runner and the project additions.
// A path appears once.
func credentialPaths(home, configDir string, extra []string) []string {
	paths := homeCredentials(home)

	if configDir != "" {
		paths = append(paths, expand(configDir, home))
	}

	paths = append(paths, expandAll(extra, home)...)

	slices.Sort(paths)

	return slices.Compact(paths)
}

// withSocket returns the deny paths with the socket of the SSH agent of the engine added. A socket is a file, so the deny list covers it.
// A socket that is a link adds the link and its target. An empty socket adds nothing.
func withSocket(deny []string, socket string) []string {
	if socket == "" {
		return deny
	}

	paths := append(slices.Clone(deny), socket)

	if target, err := filepath.EvalSymlinks(socket); err == nil {
		paths = append(paths, target)
	}

	return slices.Compact(slices.Sorted(slices.Values(paths)))
}

// withoutSocket returns env without the variable of the SSH agent.
func withoutSocket(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(pair string) bool { return strings.HasPrefix(pair, model.SSHAgentVar+"=") })
}

// socketOf returns the socket that env names, or "" when it names none.
func socketOf(env []string) string {
	for _, pair := range env {
		if socket, found := strings.CutPrefix(pair, model.SSHAgentVar+"="); found {
			return socket
		}
	}

	return ""
}

// homeCredentials returns the paths of the engine credential list under home. A pattern with a * stands for the paths that match it.
func homeCredentials(home string) []string {
	var paths []string

	for _, pattern := range model.CredentialPatterns() {
		full := filepath.Join(home, pattern)
		if !strings.Contains(pattern, "*") {
			paths = append(paths, full)

			continue
		}

		paths = append(paths, matching(full)...)
	}

	return paths
}

// matching returns the paths that a glob matches, and none for a pattern that is not valid.
func matching(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	return matches
}

// writablePaths resolves the gate_allow_write paths of a project: a relative path starts at the work folder, and ~/ is the home folder.
// A path that is a closed path, a credential path, inside one, or holds one is refused, because it would open a path that the engine keeps closed.
// The check of the names (.git, .claude, .husky, .githooks, .env*) is the one that beehest check runs, so a step and its check agree.
func writablePaths(paths []string, home, dir string, closed []string) ([]string, string) {
	out := make([]string, 0, len(paths))

	for _, path := range paths {
		if err := model.CheckAllowWrite([]string{path}); err != nil {
			return nil, fmt.Sprintf("%v. The step did not start.", err)
		}

		full := filepath.Clean(absolute(expand(path, home), dir))

		if secret := overlap(full, closed); secret != "" {
			return nil, fmt.Sprintf("The project lists the gate_allow_write path %s, which overlaps the protected path %s. The step did not start. Remove the path.", path, secret)
		}

		out = append(out, full)
	}

	return out, ""
}

// absolute starts a relative path at the work folder.
func absolute(path, dir string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(dir, path)
}

// overlap returns the protected path that full is, is inside, or holds, or "" when there is none.
func overlap(full string, protected []string) string {
	for _, secret := range protected {
		if within(secret, full) || within(full, secret) {
			return secret
		}
	}

	return ""
}

// within reports whether the rule parent covers path, as the names stand and again with every symbolic link resolved. It is the one test of what a
// rule of the sandbox covers. A rule covers its own name and each path below it. A rule with a glob character also covers each path that matches it
// and each path below such a match, as the CLI reads a rule. Names compare without case, because the default disk of the owner may not tell .Git from
// .git. This is stricter than a case-sensitive disk needs, and it is never looser.
func within(parent, path string) bool {
	return inside(parent, path) || inside(shell.RealPath(parent), shell.RealPath(path))
}

func inside(parent, path string) bool {
	rule, name := strings.ToLower(parent), strings.ToLower(path)

	return nameInside(rule, name) || matchInside(rule, filepath.Clean(name))
}

// matchInside reports whether path or a folder above it matches the pattern, by name and with case kept. A pattern that does not parse matches nothing.
func matchInside(pattern, path string) bool {
	for {
		if doublestar.MatchUnvalidated(pattern, path) {
			return true
		}

		up := filepath.Dir(path)
		if up == path {
			return false
		}

		path = up
	}
}

// nameInside reports whether path is parent or lies below it, by name and with case kept. A child named ..x lies below its parent.
func nameInside(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// expand replaces a leading ~ with the home folder.
func expand(path, home string) string {
	if path == "~" {
		return home
	}

	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}

	return path
}

func expandAll(paths []string, home string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.Clean(expand(path, home))
	}

	return out
}

// sharedGitDirs returns the common git folder of a linked worktree that a clone in found holds a record of. It reads the records of the clones, never
// the .git file or the commondir file, which a step can write. A clone, and a folder that no clone knows, have none outside themselves.
func sharedGitDirs(found gitexec.Repositories, dir string) []string {
	repo, err := found.Of(dir)
	if err != nil {
		return nil
	}

	return commonOf(repo)
}

// commonOf returns the common git folder of repo when it lies outside its git folder: repo is a linked worktree. A repository with no git folder has none.
func commonOf(repo gitexec.Repository) []string {
	if repo.GitDir == repo.CommonDir {
		return nil
	}

	return []string{repo.CommonDir}
}
