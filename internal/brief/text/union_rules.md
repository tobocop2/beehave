**A union identity is not complete until you count what came from NEITHER side.** `result - development ==
branch - base` and `result - branch == development - base` both hold for a rewrite that balances. Add
the third: lines present in the result and absent from BOTH parents must be zero, with a control that
counts lines absent from one parent alone.
**THE LINE CHECKS ARE NOT THE GATE. The union gate is a BUILD at the merge tip.** Every line can come from one
parent while the tree does not compile. Be most suspicious when a branch ADDS an implementor of a shared
trait: the base cannot have updated a file it never had. When the branch CHANGES a trait's method
signatures, enumerate that trait's implementors AT THE TIP and confirm every one is in the branch's diff.
**The third check compares FILE CONTENT, never two added-line sets.** Intersecting the `+` lines of the two
diffs matches `+}` and `+#[test]` by coincidence of text. Per changed file, the result's lines must be a
subset of the union of the two parents' versions of that file; the control is the same count against one
parent alone, which must be nonzero.
Restore a mutated file from a snapshot and **assert the blob**; `git checkout -- <file>` resets to HEAD and
silently discards other uncommitted edits.
