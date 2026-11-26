# The survival of a range: for each merge between a base and a tip, the lines that the merge added and that the tip no longer has.
# merge_survival returns one entry for each merge of the range, and the entry is a signal for a person to read, never a verdict.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//survival.star", "merge_survival").

# The count of lost lines that an entry of a file shows. The count of the entry is the whole number.
LINES_SHOWN = 10

# git exits with 128 for a file that the tip does not hold.
NO_FILE = 128

def _git(name, args, allowed):
    """Runs git in the folder of the item and returns its result. An exit code that is not 0 and not in allowed ends the call with the output of git."""
    out = command(name = name + ".git", run = ["git", "-c", "core.quotePath=false"] + args)
    if out.rc != 0 and out.rc not in allowed:
        fail("survival %s: git %s failed with exit code %d: %s" % (name, args[0], out.rc, out.stderr.strip()))
    return out

def _added(diff):
    """The lines that a -U0 diff adds, as a dict from a file to the list of its added lines. A blank line is dropped. A file that the diff removes has no entry."""
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
        elif not header and file != "" and line.startswith("+") and line[1:].strip() != "":
            added.setdefault(file, []).append(line[1:])
    return added

def _tip_lines(name, tip, file, seen):
    """The lines of the file at the tip as a dict, or None when the tip holds no such file. Each file is read once."""
    if file not in seen:
        out = _git(name, ["cat-file", "blob", tip + ":" + file], [NO_FILE])
        seen[file] = None if out.rc == NO_FILE else {line: True for line in out.stdout.split("\n")}
    return seen[file]

def merge_survival(name, base, tip):
    """Reads each merge of base..tip, oldest first. Every merge counts, not only a merge of a pull request, because a sweep of the pull request merges alone gives a confident zero.
    For a merge, the lines that it added against its first parent are checked, one by one and exactly, against the file at the tip. Returns a list with one struct for each merge:
    `merge` (the commit), `subject`, `lost` (a list of structs with `file`, `count` and `lines`, the first LINES_SHOWN lines) and `deleted` (the files that the merge added and that the tip does not hold).
    A file that is gone whole is listed in `deleted` and apart from `lost`, because it is a retirement for a person to check. A file renamed after the merge shows as deleted.
    The range is empty when base and tip are the same commit. `name` makes the step names of the call unique."""
    if name == "" or base == "" or tip == "":
        fail("merge_survival needs a `name`, a `base` and a `tip` that are not empty")

    merges = _git(name, ["rev-list", "--merges", "--reverse", base + ".." + tip], []).stdout.split()
    seen = {}
    result = []
    for m in merges:
        subject = _git(name, ["log", "-1", "--format=%s", m], []).stdout.strip()
        diff = _git(name, ["diff", "-U0", "--no-color", "--no-ext-diff", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", m + "^1", m], []).stdout
        added = _added(diff)
        lost = []
        deleted = []
        for file in sorted(added.keys()):
            held = _tip_lines(name, tip, file, seen)
            if held == None:
                deleted.append(file)
                continue

            gone = [line for line in added[file] if line not in held]
            if len(gone) > 0:
                lost.append(struct(file = file, count = len(gone), lines = gone[:LINES_SHOWN]))

        result.append(struct(merge = m, subject = subject, lost = lost, deleted = deleted))

    return result
