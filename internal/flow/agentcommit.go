package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehest/internal/proc"
)

// The outputs that an editing step adds to the ones it declares: the subject and the body of the commit the engine makes for the step.
const (
	commitSubjectField = "commit_subject"
	commitBodyField    = "commit_body"
)

const (
	// commitMessageFile and commitPathsFile are the files of the engine in the folder of the output file, never in the worktree.
	commitMessageFile = "commit-message.txt"
	commitPathsFile   = "commit-paths"
	// statusPrefix is the length of the code and the space before a path in a status line.
	statusPrefix = 3
	// nul ends each path of a list that git reads or writes with -z.
	nul = "\x00"
)

// commitMessage is the part of the output file that holds the commit message. A nil field is a field the agent left out.
type commitMessage struct {
	Subject *string `json:"commit_subject"`
	Body    *string `json:"commit_body"`
}

// commits is true when the engine commits the changes of the step: the step may edit, and the folder is a repository.
func (a agentArgs) commits() bool { return a.edits && !a.plain }

// wanted lists the names the output file may hold: the declared outputs and, for a step that commits, the commit message.
func (a agentArgs) wanted() []string {
	if !a.commits() {
		return a.declared
	}

	wanted := slices.Clone(a.declared)

	for _, name := range []string{commitSubjectField, commitBodyField} {
		if !slices.Contains(wanted, name) {
			wanted = append(wanted, name)
		}
	}

	return wanted
}

// readCommit reads the commit message of a finished step from its output file into outputs, and returns what is wrong with it.
// A step that wrote no subject has no message: the engine makes no commit for it.
func (a agentArgs) readCommit(ctx context.Context, outputs map[string]any) (string, error) {
	if !a.commits() || outputs["stop"] != "" {
		return "", nil
	}

	outputs[commitSubjectField], outputs[commitBodyField] = "", ""

	subject, body, problem, err := a.checkedMessage(ctx)
	if problem != "" || err != nil || subject == "" {
		return problem, err
	}

	outputs[commitSubjectField], outputs[commitBodyField] = subject, body

	return "", nil
}

// checkedMessage reads the commit message from the output file and checks it. It returns an empty subject when the agent wrote none, and the problem
// when the message is not fit to commit.
func (a agentArgs) checkedMessage(ctx context.Context) (subject, body, problem string, err error) {
	message, problem := a.writtenMessage()
	if problem == "" {
		problem = message.problem()
	}

	if problem != "" || message.Subject == nil {
		return "", "", problem, nil
	}

	subject, body = *message.Subject, message.body()

	if problem, err = a.trailerProblem(ctx, subject, body); problem != "" || err != nil {
		return "", "", problem, err
	}

	return subject, body, "", nil
}

// problem says what is wrong with the fields of the message, or returns "" when they are fit to commit or both are left out.
func (m commitMessage) problem() string {
	if m.Subject == nil && m.Body != nil {
		return "the output file has " + commitBodyField + " and no " + commitSubjectField + ". Write both"
	}

	if m.Subject != nil {
		return subjectProblem(*m.Subject)
	}

	return ""
}

// body is the body of the message, empty when the agent left it out.
func (m commitMessage) body() string {
	if m.Body == nil {
		return ""
	}

	return *m.Body
}

// writtenMessage reads the commit message from the output file. A missing or empty file holds none.
func (a agentArgs) writtenMessage() (commitMessage, string) {
	raw, err := os.ReadFile(a.file)
	if err != nil || len(raw) == 0 {
		return commitMessage{}, ""
	}

	var message commitMessage
	if err = json.Unmarshal(raw, &message); err != nil {
		return commitMessage{}, fmt.Sprintf("the output file %s must be a JSON object with %s and %s as text: %v", a.file, commitSubjectField, commitBodyField, err)
	}

	return message, ""
}

// subjectProblem says what is wrong with a commit subject, or returns "" for one line of text.
func subjectProblem(subject string) string {
	if strings.TrimSpace(subject) == "" || strings.ContainsAny(subject, "\r\n") {
		return commitSubjectField + " must be one line of text that is not empty"
	}

	return ""
}

// trailerProblem asks git whether it reads a trailer from the message, and returns the refusal when it does.
func (a agentArgs) trailerProblem(ctx context.Context, subject, body string) (string, error) {
	file, err := a.writeMessage(subject, body)
	if err != nil {
		return "", err
	}

	out, err := git(ctx, a.git, "interpret-trailers", "--parse", file)
	if err != nil {
		return "", fmt.Errorf("check the commit message for a trailer: %w", err)
	}

	if strings.TrimSpace(out) != "" {
		return "the commit body must not end with a trailer such as Co-Authored-By. Write plain sentences in " + commitBodyField, nil
	}

	return "", nil
}

// writeMessage writes the commit message to its file in the folder of the output file and returns the path.
func (a agentArgs) writeMessage(subject, body string) (string, error) {
	file := filepath.Join(filepath.Dir(a.file), commitMessageFile)
	text := subject + "\n"

	if strings.TrimSpace(body) != "" {
		text += "\n" + strings.TrimRight(body, "\n") + "\n"
	}

	if err := os.WriteFile(file, []byte(text), dataMode); err != nil {
		return "", fmt.Errorf("write the commit message: %w", err)
	}

	return file, nil
}

// agentAndCommit runs the agent of a call up to its tries, then the commit of the engine, and returns the outputs of the call.
func (s *script) agentAndCommit(name string, a agentArgs, in PhaseInput) (map[string]any, error) {
	done, err := s.attempts(name, a, in)
	if err != nil {
		return nil, err
	}

	return s.commit(name, a, done.Outputs)
}

// commit runs the step that commits the changes of an editing step, in a recorded step of its own, and returns the outputs of the call with the new head.
func (s *script) commit(name string, a agentArgs, outputs map[string]any) (map[string]any, error) {
	if !a.commits() {
		return outputs, nil
	}

	made, err := runStep(s.e, s.ctx, name+"/commit", func(ctx context.Context) (stepResult, error) {
		return a.commitStep(ctx, outputs)
	})
	if err != nil {
		return nil, err
	}

	merged := maps.Clone(outputs)
	maps.Copy(merged, made.Outputs)

	return merged, nil
}

// commitStep commits the paths that the step changed with the message of the step. It makes no commit when the step wrote no subject or changed no path,
// so a run of it after a commit that no record followed finds a clean folder and commits nothing again.
func (a agentArgs) commitStep(ctx context.Context, outputs map[string]any) (stepResult, error) {
	subject, _ := outputs[commitSubjectField].(string) //nolint:errcheck // a call with no subject has none
	if subject == "" {
		return stepResult{}, nil
	}

	head, err := a.engineCommit(ctx, outputs, subject)
	if err != nil || head == "" {
		return stepResult{}, err
	}

	return stepResult{Outputs: map[string]any{"head_sha": head, "committed": true}}, nil
}

// engineCommit returns the engine commit of the step: the one that an earlier run made, or the one that it makes now. It returns "" when the step changed nothing.
func (a agentArgs) engineCommit(ctx context.Context, outputs map[string]any, subject string) (string, error) {
	anchor, _ := outputs["head_sha"].(string) //nolint:errcheck // a call with no head has no anchor

	earlier, err := a.earlierCommit(ctx, anchor, subject)
	if err != nil || earlier != "" {
		return earlier, err
	}

	paths, err := a.changedPaths(ctx)
	if err != nil || len(paths) == 0 {
		return "", err
	}

	body, _ := outputs[commitBodyField].(string) //nolint:errcheck // a call with no body has none

	return a.commitPaths(ctx, paths, subject, body)
}

// EngineName and EngineEmail are the author and the committer of every commit that the engine makes for a step, and of the union commits of merge.
// They are the identity of beehest and never the user.name and user.email of a clone, which an agent can change.
const (
	EngineName  = "beehest"
	EngineEmail = "beehest@localhost"
)

// earlierCommit returns HEAD when an earlier run of this step made it and no record followed, and "" otherwise.
// The anchor is the head after the agent ended, which the call recorded: it is the start head, or the commit of the agent when the agent also committed.
// HEAD is the engine commit of this step when its parent is the anchor, and its author, its committer and its subject are those of the step.
func (a agentArgs) earlierCommit(ctx context.Context, anchor, subject string) (string, error) {
	head, err := folderHead(ctx, a.git)
	if err != nil || head == anchor || anchor == "" {
		return "", err
	}

	out, err := git(ctx, a.git, "log", "-1", "--format=%P%x00%an%x00%ae%x00%cn%x00%ce%x00%s", head)
	if err != nil {
		return "", fmt.Errorf("read the last commit of the item folder: %w", err)
	}

	fields := strings.Split(strings.TrimRight(out, "\n"), nul)
	mine := []string{anchor, EngineName, EngineEmail, EngineName, EngineEmail, subject}

	if !slices.Equal(fields, mine) {
		return "", nil
	}

	return head, nil
}

// change is one line of git status: its two-letter code and its path.
type change struct {
	code, path string
}

// changedPaths lists the paths that the step changed. An untracked file whose size and time are those of the start mark is not the step's.
// A tracked file that was already changed at the start mark is the step's only when its content is not the content at the start.
func (a agentArgs) changedPaths(ctx context.Context) ([]string, error) {
	out, err := git(ctx, a.git, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("list the files that the step changed: %w", err)
	}

	root, err := topLevel(ctx, a.git)
	if err != nil {
		return nil, err
	}

	return a.ownPaths(ctx, root, parseStatus(out))
}

// ownPaths returns the paths of the changes that are the step's: each change that was not already in the folder at the start mark.
func (a agentArgs) ownPaths(ctx context.Context, root string, changes []change) ([]string, error) {
	var paths []string

	for _, one := range changes {
		unchanged, err := a.unchangedSinceStart(ctx, root, one)
		if err != nil {
			return nil, err
		}

		if !unchanged {
			paths = append(paths, one.path)
		}
	}

	return paths, nil
}

// unchangedSinceStart tells whether a change of the status was already in the folder at the start mark and the step left it as it was.
func (a agentArgs) unchangedSinceStart(ctx context.Context, root string, one change) (bool, error) {
	if one.code == "??" {
		return a.start.Seen[one.path] == stamp(root, one.path), nil
	}

	before, dirty := a.start.Dirty[one.path]
	if !dirty {
		return false, nil
	}

	now, err := contentHash(ctx, a.git, root, one.path)

	return now == before, err
}

// topLevel returns the root of the repository of the folder.
func topLevel(ctx context.Context, tree proc.Runner) (string, error) {
	root, err := git(ctx, tree, revParse, "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find the root of the item folder: %w", err)
	}

	return strings.TrimSpace(root), nil
}

// dirtyTracked maps each tracked path that differs from HEAD, in the index or in the files, to the hash of its content now.
func dirtyTracked(ctx context.Context, tree proc.Runner) (map[string]string, error) {
	out, err := git(ctx, tree, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("list the tracked files that are changed at the start: %w", err)
	}

	root, err := topLevel(ctx, tree)
	if err != nil {
		return nil, err
	}

	return hashAll(ctx, tree, root, strings.Split(out, nul))
}

// hashAll maps each name to the state of its file. An empty name, which the end of a list that ends with a NUL byte leaves, is skipped.
func hashAll(ctx context.Context, tree proc.Runner, root string, names []string) (map[string]string, error) {
	hashes := map[string]string{}

	for _, name := range names {
		if name == "" {
			continue
		}

		var err error

		if hashes[name], err = contentHash(ctx, tree, root, name); err != nil {
			return nil, err
		}
	}

	return hashes, nil
}

// contentHash is the state of a file of the folder: `gone` for a file that is not there, `link:` and the target for a symbolic link, and otherwise the
// executable bit and the git hash of the content, so a change of the mode alone is a change.
func contentHash(ctx context.Context, tree proc.Runner, root, name string) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(name))

	info, err := os.Lstat(path)
	if err != nil {
		return "gone", nil //nolint:nilerr // a file that is not there has no content to hash
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(path)

		return "link:" + target, linkErr
	}

	out, err := git(ctx, tree, "hash-object", "--", path)
	if err != nil {
		return "", fmt.Errorf("hash the file %s: %w", name, err)
	}

	return strconv.FormatBool(info.Mode()&executable != 0) + ":" + strings.TrimSpace(out), nil
}

// executable is the owner execute bit of a file mode.
const executable = 0o100

// parseStatus reads the output of git status --porcelain=v1 -z. A renamed or copied path is followed by its old path, which is not a change of its own.
func parseStatus(out string) []change {
	var changes []change

	entries := strings.Split(out, nul)

	for i := 0; i < len(entries); i++ {
		if len(entries[i]) <= statusPrefix {
			continue
		}

		changes = append(changes, change{code: entries[i][:2], path: entries[i][statusPrefix:]})

		if strings.ContainsAny(entries[i][:2], "RC") {
			i++
		}
	}

	return changes
}

// stage writes the list of the paths to a file and stages them by name. It returns the file, which the commit reads too.
// The paths go through a file, so no path is parsed by a shell or taken for a pattern.
func (a agentArgs) stage(ctx context.Context, paths []string) (string, error) {
	list := filepath.Join(filepath.Dir(a.file), commitPathsFile)
	if err := os.WriteFile(list, []byte(strings.Join(paths, nul)+nul), dataMode); err != nil {
		return "", fmt.Errorf("write the list of paths to commit: %w", err)
	}

	if _, err := git(ctx, a.git, "--literal-pathspecs", "add", "--pathspec-from-file="+list, "--pathspec-file-nul"); err != nil {
		return "", fmt.Errorf("stage the files that the step changed: %w", err)
	}

	return list, nil
}

// commitPaths stages the paths by name and commits them with the identity of the engine. It returns the new head.
func (a agentArgs) commitPaths(ctx context.Context, paths []string, subject, body string) (string, error) {
	list, err := a.stage(ctx, paths)
	if err != nil {
		return "", err
	}

	message, err := a.writeMessage(subject, body)
	if err != nil {
		return "", err
	}

	// The identity is the one of the engine, passed on the command itself. The engine never reads it from the git config of the clone, which an agent can change.
	// --only with the same list commits these paths and no other path that is staged.
	commit := []string{
		"--literal-pathspecs", "-c", "user.name=" + EngineName, "-c", "user.email=" + EngineEmail,
		"commit", "--cleanup=verbatim", "-F", message, "--only", "--pathspec-from-file=" + list, "--pathspec-file-nul",
	}

	if _, err = git(ctx, a.git, commit...); err != nil {
		return "", fmt.Errorf("commit the files that the step changed: %w", err)
	}

	return folderHead(ctx, a.git)
}
