package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/gitexec"
)

// sandboxPlan makes the plan for the sandbox of one run, or returns the reason why the step cannot start with it.
// The shell may read the work folder, the shared git directories of a linked work tree and the read paths of the project, and nothing else under the home folder.
// The credential paths come from the engine list, the project file and the config folder of the runner. The .env files of the work folder, at any depth
// and whether or not they exist yet, are denied by a pattern that the CLI matches when the file is used.
func (a agents) sandboxPlan(ctx context.Context, request flow.AgentRequest, runner model.Runner, dir string) (agent.SandboxPlan, string) {
	home, err := a.home()
	if err != nil {
		return agent.SandboxPlan{}, "obee cannot find the home folder, so the sandbox cannot deny it. The step did not start."
	}

	project := a.projects[request.Project]
	deny := credentialPaths(home, runner.ConfigDir, project.SandboxDeny)
	read := expandAll(project.SandboxRead, home)

	closed := closedPaths(ctx, dir, home)

	write, note := checkedPaths(request.Sandbox.AllowWrite, read, home, dir, deny, closed)
	if note != "" {
		return agent.SandboxPlan{}, note
	}

	return agent.SandboxPlan{
		Work: dir, GitDirs: sharedGitDirs(dir), Read: read, Deny: deny, DenyPatterns: envPatterns(dir), DenyWrite: writeDenies(dir, closed),
		Network: request.Sandbox.Network, AllowWrite: write,
	}, ""
}

// checkedPaths returns the allow_write paths of the step, resolved, or the reason why the step cannot start with its write or read paths.
func checkedPaths(allowWrite, read []string, home, dir string, deny, closed []string) ([]string, string) {
	if note := readNote(read, home, deny); note != "" {
		return nil, note
	}

	return writablePaths(allowWrite, home, dir, slices.Concat(deny, closed))
}

// writeDenies returns the closed paths that the adapter denies for write: the list that allow_write is checked against, less the settings folder of
// the agent CLI, which the CLI writes itself, and less a .git that is a file (a linked work tree), which holds only a pointer.
func writeDenies(dir string, closed []string) []string {
	out := make([]string, 0, len(closed))

	for _, path := range closed {
		switch {
		case path == filepath.Join(dir, ".claude"):
		case path == filepath.Join(dir, ".git") && !isFolder(path):
		default:
			out = append(out, path)
		}
	}

	return out
}

func isFolder(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// envPatterns returns the patterns of the .env files of the work folder: the top level and every depth.
func envPatterns(dir string) []string {
	return []string{filepath.Join(dir, model.EnvPrefix+"*"), filepath.Join(dir, "**", model.EnvPrefix+"*")}
}

// closedPaths returns the paths under the work folder that a step may never write: the closed names and the hooks folder that core.hooksPath names.
func closedPaths(ctx context.Context, dir, home string) []string {
	paths := make([]string, 0, len(model.ClosedNames())+1)

	for _, name := range model.ClosedNames() {
		paths = append(paths, filepath.Join(dir, name))
	}

	if hooks := hooksPath(ctx, dir, home); hooks != "" {
		paths = append(paths, hooks)
	}

	return paths
}

// hooksPath returns the hooks folder that core.hooksPath names for the repository of dir, as an absolute path, or "" when it is not set.
func hooksPath(ctx context.Context, dir, home string) string {
	result := gitexec.ReadConfig(ctx, dir, "core.hooksPath")
	if result.ExitCode != 0 {
		return ""
	}

	text := strings.TrimSpace(result.Stdout)
	if text == "" {
		return ""
	}

	return filepath.Clean(absolute(expand(text, home), dir))
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

// writablePaths resolves the allow_write paths of a step: a relative path starts at the work folder, and ~/ is the home folder.
// A path that is a closed path, a credential path, inside one, or holds one is refused, because it would open a path that the engine keeps closed.
// The check of the names (.git, .claude, .husky, .githooks, .env*) is the one that obee check runs, so a step and its check agree.
func writablePaths(paths []string, home, dir string, closed []string) ([]string, string) {
	out := make([]string, 0, len(paths))

	for _, path := range paths {
		if err := model.CheckAllowWrite([]string{path}); err != nil {
			return nil, fmt.Sprintf("%v. The step did not start.", err)
		}

		full := filepath.Clean(absolute(expand(path, home), dir))

		if secret := overlap(full, closed); secret != "" {
			return nil, fmt.Sprintf("The step lists the allow_write path %s, which overlaps the protected path %s. The step did not start. Remove the path.", path, secret)
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

// within reports whether path is parent or inside it, as the names stand and again with every symbolic link resolved. Names compare without case,
// because the default disk of the owner may not tell .Git from .git. This is stricter than a case-sensitive disk needs, and it is never looser.
func within(parent, path string) bool {
	return inside(parent, path) || inside(resolved(parent), resolved(path))
}

func inside(parent, path string) bool {
	rel, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(path))

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolved returns the path with every symbolic link resolved. A path that does not exist yet is resolved at its nearest parent that exists,
// and the names below that parent are kept as written.
func resolved(path string) string {
	path = filepath.Clean(path)

	var tail []string

	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{real}, tail...)...)
		}

		tail = append([]string{filepath.Base(current)}, tail...)
	}

	return path // the root, which is no link
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

// sharedGitDirs returns the shared git directory of a linked work tree: the folder that the file .git of the work folder points to, up to its common directory.
// A work folder with a .git folder, or with no .git, has none outside itself.
func sharedGitDirs(dir string) []string {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".git")))
	if err != nil {
		return nil
	}

	target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok {
		return nil
	}

	return []string{commonDir(absolute(target, dir))}
}

// commonDir returns the common directory of a git directory: the folder that its file commondir names, or the git directory when it has none.
func commonDir(target string) string {
	common, err := os.ReadFile(filepath.Join(target, "commondir")) //nolint:gosec // the git directory that the .git file of the work folder names
	if err != nil {
		return filepath.Clean(target)
	}

	return filepath.Clean(absolute(strings.TrimSpace(string(common)), target))
}
