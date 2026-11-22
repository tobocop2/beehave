# A supervisor and one worker. The supervisor reads what the worker agent says while it works, and cancels the worker when it runs a forbidden command.
# Run: TASK=21 beehave run <item> <project> --workflow workflow.star
# TASK is the item id of the worker. FORBIDDEN is the text that stops the worker. WATCH is how long one read waits for a new entry, such as "5s".

MODEL = "sonnet"
MAX_READS = 1000
FORBIDDEN = env("FORBIDDEN", default = "rm -rf")
WATCH = env("WATCH", default = "5s")

def worker(item):
    """One worker run. Its agent step edits nothing."""
    agent(name = "work", prompt = "prompts/work.md", model = MODEL, edits = False, inputs = {"item": item})
    return {"stage": "done"}

def main(item):
    runs = spawn(items = [env("TASK", default = "21")], entry = "worker", name = "worker")
    child = runs.runs[0]

    # Each read gives the entries after the cursor, and the next cursor. The loop stops when the worker ended and no entry is left.
    cursor = ""
    canceled = False
    seen = 0
    for turn in range(MAX_READS):
        got = stream(child, after = cursor, timeout = WATCH, name = "watch-%d" % turn)
        for entry in got.entries:
            seen += 1
            command(name = "saw-%d" % seen, run = ["echo", "saw", entry.kind, entry.text])
            if not canceled and entry.kind == "call" and FORBIDDEN in entry.text:
                command(name = "forbidden", run = ["echo", "forbidden command", entry.text])
                cancel(child, name = "stop")
                canceled = True
        cursor = got.next
        if got.ended:
            break

    command(name = "report", run = ["echo", "entries", str(seen), "canceled", str(canceled)])
