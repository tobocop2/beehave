# The survival of a range: for each merge between a base and a tip, the lines that the merge added and that the tip no longer has.
# merge_survival returns the entries for the merge commits of the range, and the entries are a signal for a person to read, never a verdict.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//survival.star", "merge_survival").

# The count of lost lines that an entry of a file shows. The count of the entry is the whole number.
LINES_SHOWN = 10

# git exits with 128 for a file that the tip does not hold.
NO_FILE = 128

# git exits with 1 when a commit is not an ancestor of another.
NOT_ANCESTOR = 1

def _git(name, args, allowed):
    """Runs git in the folder of the item and returns its result and a problem. The problem is the text of a failure, or an empty text when the exit code is 0 or in allowed.
    An error of the step is a result with rc -1, so no failure of git ends the run."""
    out = command(name = name + ".git", run = ["git", "-c", "core.quotePath=false"] + args, failure = "return")
    if out.rc == 0 or out.rc in allowed:
        return out, ""

    return out, "survival %s: git %s failed with exit code %d: %s" % (name, args[0], out.rc, (out.error if out.rc < 0 else out.stderr).strip())

def _added(diff):
    """The lines that a -U0 diff adds, as a dict from a file to the list of its added lines. A file that the diff removes has no entry."""
    added = {}
    file = ""
    header = False
    for line in diff.split("\n"):
        if line.startswith("diff --git "):
            header = True
            file = ""
        elif header and line.startswith("+++ "):
            file = "" if line == "+++ /dev/null" else line[len("+++ b/"):].rstrip("\t")
            header = False
        elif line.startswith("@@"):
            header = False
        elif not header and file != "" and line.startswith("+"):
            added.setdefault(file, []).append(line[1:])
    return added

def _synced(name, base, parents):
    """Whether each parent in `parents` is on the base, and a problem. A merge whose parents after the first are all on the base brings the base into the branch: it is a sync, not work of the round."""
    for parent in parents:
        out, problem = _git(name, ["merge-base", "--is-ancestor", parent, base], [NOT_ANCESTOR])
        if problem != "" or out.rc == NOT_ANCESTOR:
            return False, problem

    return True, ""

def _tip_lines(name, tip, file, seen):
    """The lines of the file at the tip as a dict, or None when the tip holds no such file, and a problem. Each file is read once."""
    if file not in seen:
        out, problem = _git(name, ["cat-file", "blob", tip + ":" + file], [NO_FILE])
        if problem != "":
            return None, problem

        seen[file] = None if out.rc == NO_FILE else {line: True for line in out.stdout.split("\n")}

    return seen[file], ""

def _read_merge(name, m, tip, seen):
    """The entry of one merge, and a problem."""
    named, problem = _git(name, ["log", "-1", "--format=%s", m], [])
    if problem != "":
        return None, problem

    diff, problem = _git(name, ["diff", "-U0", "--no-color", "--no-ext-diff", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", m + "^1", m], [])
    if problem != "":
        return None, problem

    added = _added(diff.stdout)
    lost = []
    deleted = []
    for file in sorted(added.keys()):
        held, problem = _tip_lines(name, tip, file, seen)
        if problem != "":
            return None, problem

        if held == None:
            deleted.append(file)
            continue

        gone = [line for line in added[file] if line not in held]
        if len(gone) > 0:
            lost.append(struct(file = file, count = len(gone), lines = gone[:LINES_SHOWN]))

    return struct(merge = m, subject = named.stdout.strip(), lost = lost, deleted = deleted), ""

def merge_survival(name, base, tip):
    """Reads each merge commit of base..tip, oldest first, and returns a struct with `merges` and `error`. A merge counts whatever its subject says, because a sweep of the pull request merges alone gives a confident zero.
    A commit that is no merge is not read, so a change that landed as a squash or a rebase has no entry. A merge that brings the base into the branch (each parent after the first is on the base) is not read either, because the base rewrites its own lines.
    For a merge, the lines that it added against its first parent are checked, one by one and exactly, against the file at the tip. `merges` is a list with one struct for each merge:
    `merge` (the commit), `subject`, `lost` (a list of structs with `file`, `count` and `lines`, the first LINES_SHOWN lines) and `deleted` (the files that the merge added and that the tip does not hold).
    A file is checked by membership: a line that the tip holds anywhere in the file counts as kept, even when only a copy of it is left, so a lost line that an equal line hides is not listed.
    A file that is gone whole is listed in `deleted` and apart from `lost`, because it is a retirement for a person to check. A file renamed after the merge shows as deleted.
    A git call that fails gives `error`, the text of the failure, and no merges. The failure never ends the run, because the entries are a signal. `error` is an empty text when the read ran.
    The range is empty when base and tip are the same commit. `name` makes the step names of the call unique."""
    if name == "" or base == "" or tip == "":
        fail("merge_survival needs a `name`, a `base` and a `tip` that are not empty")

    listed, problem = _git(name, ["rev-list", "--merges", "--parents", "--reverse", base + ".." + tip], [])
    if problem != "":
        return struct(merges = [], error = problem)

    seen = {}
    result = []
    for line in listed.stdout.split("\n"):
        ids = line.split()
        if len(ids) < 3:
            continue

        synced, problem = _synced(name, base, ids[2:])
        if problem != "":
            return struct(merges = [], error = problem)

        if synced:
            continue

        entry, problem = _read_merge(name, ids[0], tip, seen)
        if problem != "":
            return struct(merges = [], error = problem)

        result.append(entry)

    return struct(merges = result, error = "")
