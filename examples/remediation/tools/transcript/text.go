package transcript

import "strings"

// firstRunes returns the first n runes of the text.
func firstRunes(text string, n int) string {
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n])
	}

	return text
}

// isAlnum reports whether the byte is an ASCII letter or digit.
func isAlnum(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

// holdsToken reports whether the text holds the token with no word byte after it, and, when lead is set, none before it.
func holdsToken(text, token string, lead bool, word func(byte) bool) bool {
	for from := 0; from < len(text); {
		at := strings.Index(text[from:], token)
		if at < 0 {
			return false
		}

		at += from
		end := at + len(token)

		if (!lead || at == 0 || !word(text[at-1])) && (end == len(text) || !word(text[end])) {
			return true
		}

		from = at + 1
	}

	return false
}
