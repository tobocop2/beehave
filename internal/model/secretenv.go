package model

import (
	"slices"
	"strings"
)

// secretWords lists the words that mark the name of a secret. The env builtin refuses such a name.
func secretWords() []string { return []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"} }

// secretSuffixes lists the last parts of a name that mark a secret. A name ends in one of them after an underscore, so PATH and AUTHOR do not match.
func secretSuffixes() []string { return []string{"KEY", "PAT", "DSN", "AUTH"} }

// secretWholeNames lists the names that mark a secret when they are the whole name.
func secretWholeNames() []string { return []string{"PAT", "AUTH", "DSN"} }

// SecretEnvName reports whether name looks like the name of a secret, in any case.
// A name marks a secret when it ends in a secret suffix, is a whole secret name, holds PAT as a part (GH_PAT_OLD), or holds a secret word.
func SecretEnvName(name string) bool {
	upper := strings.ToUpper(name)

	return slices.Contains(secretWholeNames(), upper) ||
		strings.Contains(upper, "_PAT_") ||
		slices.ContainsFunc(secretSuffixes(), func(part string) bool { return strings.HasSuffix(upper, "_"+part) }) ||
		slices.ContainsFunc(secretWords(), func(word string) bool { return strings.Contains(upper, word) })
}
