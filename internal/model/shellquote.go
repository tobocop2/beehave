package model

import "strings"

// safeWordRune holds the characters that a shell word may hold without a quote.
const safeWordRune = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// ShellQuote returns the word as one word for a POSIX shell. A word of safe characters stays bare; any other word, and the empty word, goes in single quotes.
func ShellQuote(word string) string {
	if word != "" && strings.Trim(word, safeWordRune) == "" {
		return word
	}

	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
