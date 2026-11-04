package shell

import "slices"

// The git subcommands that more than one rule names.
const (
	subStash  = "stash"
	subBranch = "branch"
)

// alwaysWrites reports whether a git subcommand changes the tree, the index, a ref, the history or a remote whatever its arguments.
func alwaysWrites(sub string) bool {
	return slices.Contains([]string{
		"add", "stage", subCommit, "push", subSendPack, "apply", "am", "merge", "rebase", "cherry-pick", "revert", "reset", "restore", "rm", "mv", "clean", "pull",
		"checkout", "switch", "update-ref", "commit-tree", "init", "clone",
	}, sub)
}

// changesRepository reports whether a git command writes the tree, the index, a ref, the configuration, the history or a remote. A reviewer fixes, commits and pushes with these.
// The commands `worktree add` and `worktree list` are not in it: a reviewer may build its own copy of the tree with `worktree add`.
func changesRepository(sub string, set flagSet) bool {
	switch sub {
	case subStash:
		return !firstIs(set, "list", "show")
	case subBranch, "tag":
		return writesRefs(sub, set)
	case cmdConfig:
		return writesConfig(set)
	case "remote":
		return firstIs(set, "add", "remove", "rm", "rename", "set-url", "set-head", "set-branches", "prune", "update")
	case "notes":
		return len(set.pos) > 0 && !firstIs(set, "list", "show", "get-ref")
	case "submodule":
		return firstIs(set, "update")
	}

	return alwaysWrites(sub)
}

// firstIs reports whether the first positional word is one of the names.
func firstIs(set flagSet, names ...string) bool {
	return len(set.pos) > 0 && slices.Contains(names, set.pos[0])
}

// listsRefs reports whether a branch or tag command only lists.
func listsRefs(set flagSet) bool {
	return set.has("l", "--list") || slices.ContainsFunc([]string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"}, func(long string) bool {
		return hasLong(set, long)
	})
}

// writesRefs reports whether a `git branch` or `git tag` command deletes, moves, copies or creates a ref. The letters and the long options are the ones that write.
func writesRefs(sub string, set flagSet) bool {
	letters := []string{"d", "a", "s", "u", "f", "m", "F"}
	longs := []string{"--delete", "--annotate", "--sign", "--local-user", forceOption, "--message", "--file"}

	if sub == subBranch {
		letters = []string{"d", "D", "m", "M", "c", "C", "f", "u"}
		longs = []string{"--delete", "--move", "--copy", forceOption, "--set-upstream-to", "--unset-upstream", "--edit-description"}
	}

	if slices.ContainsFunc(letters, func(letter string) bool { return set.has(letter, "") }) ||
		slices.ContainsFunc(longs, func(long string) bool { return hasLong(set, long) }) {
		return true
	}

	return len(set.pos) > 0 && !listsRefs(set)
}

// writesConfig reports whether a `git config` command sets, unsets or edits a value. A read gives a name, or a list or get option.
func writesConfig(set flagSet) bool {
	switch {
	case firstIs(set, "get", "list"):
		return false
	case firstIs(set, "set", "unset", "edit", "rename-section", "remove-section"), set.has("e", ""):
		return true
	}

	for _, option := range []string{"--unset", "--unset-all", "--add", "--replace-all", "--edit", "--rename-section", "--remove-section"} {
		if hasLong(set, option) {
			return true
		}
	}

	return len(set.pos) >= 2 && !hasLong(set, "--get") && !hasLong(set, "--get-all") && !hasLong(set, "--get-regexp")
}
