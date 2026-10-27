**A BINARY FIXTURE MUST BE ASSERTED INVALID UTF-8 *AND* ASSERTED TO TAKE THE REFUSAL IT WAS WRITTEN FOR.**
Assert the bytes are what you think (`from_utf8` errors) and that the path you are testing actually rejects
them; either assertion alone lets a fixture drift into proving nothing.
**A restore is not green until something forces the rebuild.** `touch` after every restore,
unconditionally, whatever the hash says, and re-run clean: a plain `cp` can land inside the same mtime
second and cargo reuses the mutated test binary.
Name targets `-p <crate> --test <name>` and **assert a NONZERO count**; a run that selects nothing exits 0.
**Name the FEATURES beside the target, every time**: a module behind a conditional-compilation flag is invisible
to a target that does not name the feature. Read the package's feature list and default first, and if the count
is zero suspect the feature list before the code. The profile lists the packages that need one.
**Read the package name out of its own manifest, never from the repository or the directory name.** A wrong
package name answers rc=101 with no tests; piped through `tail`, the rc you read is `tail`'s zero.
