## Evidence rules that are not optional

**Every absence check runs with its positive control IN THE SAME CALL.** Two empty results in one call is a
broken instrument; separately, an empty result reads as a finding.
**Never `2>/dev/null` a command whose empty output you intend to treat as evidence.**
**A control must exercise the layer the product uses**, not a layer near it.
**A verification derived from the same pattern as the edit cannot see the edit's mistake.**
**AN ISSUE'S ROOT CAUSE AGES WITH ITS PINNED DEPENDENCIES.** Re-derive the mechanism in the DEPENDENCY's
source, not only the line numbers in ours: check the pinned version in the manifest against the one the
issue was filed against before you inherit its mechanism.
**Before asserting what a runtime path does, LIST THE FILES IT TRAVERSES and name which one you read.**
When a claim depends on a branch, find where its variable is SET and not only where it is tested.
