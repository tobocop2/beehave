# The scope check of the remediation method: the closing issues of a pull request are fixed at its first review, and a merge holds for the owner when a new one appears.
# fix_scope records the set at the first review. hold_scope compares it with the set now, asks the owner about each new issue with wait, and returns the members that may land.

FIRST_READ = "scope"
MAX_NUMBER = 9223372036854775807
NAME_CHARS = "abcdefghijklmnopqrstuvwxyz0123456789_.-"
DIGITS = "0123456789"

def fix_scope(name, item = ""):
    """Fixes the scope of the item and returns struct(found, issues, off).

    An earlier read of this run or of an earlier run of the item that found a pull request is the scope: nothing is read. Otherwise one pr_closing read is the scope.
    name is the name of the call, and hold_scope finds the scope by it (its argument `first`). found is false when the item has no pull request yet: call fix_scope again later.
    off is true when the project has no forge, so there is no scope to check.
    """
    for read in earlier_reads(kind = "pr_closing", name = name, item = item, own_run = True).reads:
        if read["found"]:
            return struct(found = True, issues = read["issues"], off = False)

    read = pr_closing(name = name, item = item)
    return struct(found = read.found, issues = read.issues, off = not read.forge)

def hold_scope(name, prs, run = "", first = FIRST_READ):
    """Checks each member of prs against its scope and returns struct(prs, rejected).

    prs is a list of the members of a merge. The result prs holds the members that may land: each one has the field kept (the issues that the owner kept for it) and,
    when its scope is known, the field closing (the closing issues that were read now, which the merge checks again). rejected holds struct(item, reason) for each member that the owner did not keep.
    A member with no scope of its first review has nothing to compare: it passes with no read. first is the name that fix_scope got. name makes the step names of the call unique.
    run is the item of the run, which the owner names in beehave answer. An empty run is the item of the first member.
    A read that fails, or a pull request that is gone, ends the run.
    """
    landing = []
    rejected = []

    for pr in prs:
        then = _first_review(first, pr.item)
        if then == None:
            landing.append(_member(pr, [], None))
            continue

        now = pr_closing(name = name + "-" + pr.item, item = pr.item)
        if not now.found:
            fail("scope check of item %s: read the issues that the pull request of item %s closes: the item has no pull request" % (pr.item, pr.item))

        kept = _kept_before(pr.item)
        fresh = [issue for issue in now.issues if issue not in then and issue not in kept]
        if fresh:
            owner_item = run or prs[0].item
            named = keep_issues(wait(question = _question(pr.item, fresh, then, owner_item), name = "keep-" + pr.item).answer)
            missing = [issue for issue in fresh if issue not in named]
            if missing:
                rejected.append(struct(item = pr.item, reason = _refusal(pr.item, missing, owner_item)))
                continue

            kept = _union(kept, fresh)

        landing.append(_member(pr, kept, now.issues))

    return struct(prs = landing, rejected = rejected)

def keep_issues(text):
    """Returns the issues of an owner line in the form "keep #N[, owner/repo#N ...]", and an empty list for any other text. The word keep takes any case."""
    if not text.strip().lower().startswith("keep"):
        return []

    rest = text.strip()[4:]
    if rest == rest.lstrip():
        return []

    issues = []
    for part in rest.split(","):
        issue = _issue(part.strip())
        if issue == "":
            return []
        issues.append(issue)

    return issues

def _issue(text):
    """The issue that a text names as "#N" or "owner/repo#N" (the repository in lower case), or an empty text."""
    repo, hash, digits = text.partition("#")
    if hash == "" or digits == "" or [c for c in digits.elems() if c not in DIGITS] or int(digits) > MAX_NUMBER:
        return ""

    if repo == "":
        return "#%d" % int(digits)

    owner, slash, name = repo.lower().partition("/")
    if slash == "" or owner == "" or name == "" or [c for c in (owner + name).elems() if c not in NAME_CHARS]:
        return ""

    return "%s#%d" % (repo.lower(), int(digits))

def _first_review(first, item):
    """The issues of the first review of the item, or None when the item has none."""
    for read in earlier_reads(kind = "pr_closing", name = first, item = item, own_run = True).reads:
        if read["found"]:
            return read["issues"]

    return None

def _kept_before(item):
    """The issues that the owner kept for the item in this run and in the earlier runs of the run item."""
    kept = []
    for read in earlier_reads(kind = "wait", name = "keep-" + item, own_run = True).reads:
        kept = _union(kept, keep_issues(read["answer"]))

    return kept

def _union(left, right):
    return left + [issue for issue in right if issue not in left]

def _member(pr, kept, closing):
    """The member with the field kept, and with the field closing when the set is known."""
    fields = {name: getattr(pr, name) for name in dir(pr)}
    fields["kept"] = kept
    if closing != None:
        fields["closing"] = closing

    return struct(**fields)

def _issue_list(issues):
    return " ".join(issues) if issues else "no issue"

def _question(item, fresh, then, run):
    return "The pull request of item %s closes %s now, and did not at its first review (then: %s). Answer in the form \"keep #N[, #N ...]\", for example \"keep %s\". Any other answer rejects item %s from this merge. The other members still land. Answer with beehave answer %s." % (item, _issue_list(fresh), _issue_list(then), ", ".join(fresh), item, run)

def _refusal(item, missing, run):
    return "the pull request closes an issue that is new since its first review: item %s: %s has no owner line, so this merge rejects it and the other members still land. To keep it, run the merge again and answer with beehave answer %s \"keep %s\". Or remove the closing keyword" % (item, _issue_list(missing), run, ", ".join(missing))
