# The weekly report. The body of the item is {"week"}. The output is a text file and a comment on the status issue.
# Set UPKEEP_HOME to the folder of the example (default ~/.upkeep).

SCRIPTS = env("UPKEEP_HOME", default = env("HOME") + "/.upkeep") + "/scripts/"
TIMEOUT = "5m"
STATUS = struct(CLOSED = "closed", HOLD = "hold")

def sh(name, script, week):
    return command(name = name, run = ["sh", SCRIPTS + script, week], timeout = TIMEOUT)

def report_failed(item, made):
    item_comment(name = "failed", id = item.id, key = "failed", text = "The report script failed with exit code %d." % made.rc)
    item_status(name = "hold", id = item.id, status = STATUS.HOLD)

def publish(item, made, posted):
    item_comment(name = "report", id = item.id, key = "report", text = made.stdout + "\n\nPost: " + posted.stdout.strip())
    item_status(name = "close", id = item.id, status = STATUS.CLOSED if posted.rc == 0 else STATUS.HOLD)

def main(item):
    week = json.decode(item.body)["week"]
    made = sh("report", "report.sh", week)
    if made.rc != 0:
        return report_failed(item, made)
    publish(item, made, sh("post", "post.sh", week))
