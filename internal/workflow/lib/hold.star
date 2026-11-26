# The hold of an item that needs the owner: a question, the options and a recommendation, on the item and with the status hold.
# The helper does not call wait. A wait blocks the run, and the method keeps the queue going while the item waits.

def _text(value, what):
    """The trimmed text of a field of the note. A field that is not text, or that is blank, is refused with the name of the field."""
    if type(value) != "string" or value.strip() == "":
        fail("REFUSE: the hold note has no %s. Write the question, at least two options and a recommendation, each as non-blank text." % what)
    return value.strip()

def _decode(raw):
    """The note as a dict. Text that is not JSON, and JSON that is not an object, are refused with the start of the text."""
    decoded = json.decode(raw, default = ("invalid",))
    if type(decoded) != "dict":
        points = list(raw.codepoints())
        shown = raw if len(points) <= 200 else "".join(points[:200]) + "..."
        fail("REFUSE: the hold note is not a JSON object: %r. Write an object with the keys question, options and recommendation." % shown)
    return decoded

def _options(note):
    """The list of options of the note: at least two, each non-blank text."""
    options = note.get("options")
    if type(options) != "list" or len(options) < 2:
        fail("REFUSE: the hold note has fewer than two options. A hold with one option asks the owner nothing.")
    return [_text(option, "option %d" % (n + 1)) for n, option in enumerate(options)]

def hold_item(name, id, raw, forbid_text = []):
    """Puts the item on hold with the note that an agent wrote, and returns the text of the note.

    raw is the JSON text of one object with the keys question, options (a list of at least two texts) and recommendation. A note that lacks one of them is refused
    with an error that names the field, and then the call writes no comment and no status. A good note is written with item_comment under the key hold-note, then item_status sets the status hold.
    name makes the step names of the call unique. forbid_text is passed to item_comment.
    """
    note = _decode(raw)
    question = _text(note.get("question"), "question")
    options = _options(note)
    recommendation = _text(note.get("recommendation"), "recommendation")

    lines = ["Needs the owner: " + question, "", "Options:"]
    lines += ["%d. %s" % (n + 1, option) for n, option in enumerate(options)]
    lines += ["", "Recommendation: " + recommendation]
    text = "\n".join(lines)

    item_comment(name = name + "-note", id = id, key = "hold-note", text = text, forbid_text = forbid_text)
    item_status(name = name + "-status", id = id, status = "hold")
    return text
