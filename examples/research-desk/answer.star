# The answer of the person who reviews the draft: "approve", or "redo N: a note" for section N. Any other text is asked again.
ANSWER = struct(APPROVE = "approve", REDO = "redo", BAD = "bad")

def parse_answer(text, count):
    """Returns a struct with kind, n, note and problem. Only a redo has n and note; only a bad answer has a problem."""
    said = text.strip()
    if said.lower().startswith(ANSWER.APPROVE):
        return struct(kind = ANSWER.APPROVE, n = 0, note = "", problem = "")
    if said.lower().startswith(ANSWER.REDO + " "):
        head, _, note = said[len(ANSWER.REDO) + 1:].partition(":")
        number = head.strip()
        if number.isdigit() and 1 <= int(number) and int(number) <= count:
            return struct(kind = ANSWER.REDO, n = int(number), note = note.strip(), problem = "")
        return struct(kind = ANSWER.BAD, n = 0, note = "", problem = "There is no section %s. The sections are 1 to %d." % (number, count))
    return struct(kind = ANSWER.BAD, n = 0, note = "", problem = "Answer approve, or redo N: a note.")
