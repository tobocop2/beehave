package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

// exitRefused is the exit code of a pinned call that git did not run: the folder is unknown, the clone config cannot be read, or it holds a setting
// outside the allow list.
const exitRefused = 128

const (
	dotGit    = ".git"
	records   = "worktrees"
	gitdirRef = "gitdir"
)

// ErrUnknownRepository is the error for a folder that is neither a clone nor a worktree that a clone of the engine holds a record of.
var ErrUnknownRepository = errors.New("the folder is not a clone or a worktree that beehave made")

// Repository is a git repository that the engine names: its git dir, its common dir and its work tree.
type Repository struct {
	GitDir    string
	CommonDir string
	WorkTree  string
}

// SettingError is the refusal of a clone whose config holds settings outside the allow list.
type SettingError struct {
	Clone string
	Keys  []string
}

// Error names the settings and the command that removes them.
func (e *SettingError) Error() string {
	return fmt.Sprintf("the git config of %s holds %s, which beehave does not allow, so beehave runs no git there. Remove each with git config --unset <setting> in %s.",
		e.Clone, strings.Join(e.Keys, ", "), e.Clone)
}

// refused is the result of a call that git did not run, with the reason err.
func refused(err error) model.CommandResult {
	return model.CommandResult{ExitCode: exitRefused, Stderr: err.Error(), SetupErr: err}
}

// env returns the variables that name the repository to git.
func (r Repository) env() []string {
	return []string{"GIT_DIR=" + r.GitDir, "GIT_COMMON_DIR=" + r.CommonDir, "GIT_WORK_TREE=" + r.WorkTree}
}

// check refuses the repository when its config holds a setting outside the allow list. The listing reads no include and starts no program.
func (r Repository) check(ctx context.Context, timeout time.Duration) error {
	config := filepath.Join(r.CommonDir, "config")
	listing := start(New(r.WorkTree, timeout).ExecRunner, false).
		Run(ctx, []string{"config", "--file", config, "--list", "--name-only", "--null", "--no-includes"})

	if listing.ExitCode != 0 {
		return fmt.Errorf("read the git settings of %s: %s", filepath.Dir(r.CommonDir), strings.TrimSpace(listing.Stderr))
	}

	if outside := outsideKeys(listing.Stdout); len(outside) > 0 {
		return &SettingError{Clone: filepath.Dir(r.CommonDir), Keys: outside}
	}

	return nil
}

// outsideKeys returns each key of a listing that is not on the allow list, once. Each key of the listing ends with a NUL byte, because a subsection
// can hold a space.
func outsideKeys(listing string) []string {
	var outside []string

	for key := range strings.FieldsFuncSeq(listing, func(r rune) bool { return r == 0 }) {
		if !allowed(key) && !slices.Contains(outside, key) {
			outside = append(outside, key)
		}
	}

	return outside
}

// allowed reports whether git clone, git worktree add or a local identity writes key. Every other setting is refused.
func allowed(key string) bool {
	switch key {
	case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode",
		"core.symlinks", "extensions.objectformat", "extensions.refstorage", "extensions.relativeworktrees", "user.name", "user.email":
		return true
	}

	section, rest, _ := strings.Cut(key, ".")
	cut := strings.LastIndex(rest, ".")

	if cut <= 0 {
		return false
	}

	switch section + rest[cut:] {
	case "remote.url", "remote.fetch", "branch.remote", "branch.merge":
		return true
	}

	return false
}

// Repositories finds the repository of a folder among the clones in one folder.
type Repositories struct {
	root string
}

// NewRepositories returns the finder for the clones in root.
func NewRepositories(root string) Repositories {
	return Repositories{root: root}
}

// Clone returns the repository of the clone at dir, which must be a folder of the root.
func (r Repositories) Clone(dir string) (Repository, error) {
	root, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return Repository{}, fmt.Errorf("find the clones: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Dir(resolved) != root {
		return Repository{}, fmt.Errorf("%w: %s", ErrUnknownRepository, dir)
	}

	git := filepath.Join(resolved, dotGit)
	if info, err := os.Lstat(git); err != nil || !info.IsDir() {
		return Repository{}, fmt.Errorf("%w: %s", ErrUnknownRepository, dir)
	}

	return Repository{GitDir: git, CommonDir: git, WorkTree: resolved}, nil
}

// Of returns the repository of dir: a clone of the root, or a linked worktree whose record one clone holds. It reads the records of the clones and never
// a file in dir.
func (r Repositories) Of(dir string) (Repository, error) {
	if repo, err := r.Clone(dir); err == nil {
		return repo, nil
	}

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Repository{}, fmt.Errorf("%w: %s", ErrUnknownRepository, dir)
	}

	found := r.records(filepath.Join(resolved, dotGit))
	if len(found) != 1 {
		return Repository{}, fmt.Errorf("%w: %s", ErrUnknownRepository, dir)
	}

	found[0].WorkTree = resolved

	return found[0], nil
}

// records returns the worktree records of the clones whose gitdir file names gitfile.
func (r Repositories) records(gitfile string) []Repository {
	var found []Repository

	for _, clone := range entriesOf(r.root) {
		common := filepath.Join(r.root, clone.Name(), dotGit)

		for _, entry := range entriesOf(filepath.Join(common, records)) {
			record := filepath.Join(common, records, entry.Name())

			named, err := os.ReadFile(filepath.Join(record, gitdirRef)) //nolint:gosec // a record of a clone in the root
			if err == nil && resolvedFrom(record, strings.TrimSpace(string(named))) == gitfile {
				found = append(found, Repository{GitDir: record, CommonDir: common})
			}
		}
	}

	return found
}

// resolvedFrom returns the .git file that the gitdir file of record names. Git writes a relative name, from the record, when worktree.useRelativePaths
// is set. A relative name whose file is gone names nothing.
func resolvedFrom(record, named string) string {
	if filepath.IsAbs(named) {
		return named
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(record, named))
	if err != nil {
		return ""
	}

	return resolved
}

// entriesOf returns the entries of dir, or none when dir cannot be read: a root or a clone that cannot be read holds no record.
func entriesOf(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	return entries
}
