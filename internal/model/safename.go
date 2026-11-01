package model

import (
	"fmt"
	"strings"
)

// SafeName makes a name safe to use in a path that the engine hands to an agent.
// It keeps letters, digits and "-". Every other byte, including "_", ".", "/", "#", a space and a quote, becomes "_" and two hex digits.
// So the result holds only letters, digits, "-" and "_", it never holds a separator or a dot, and two names never share a result.
// Every folder or file name for an agent goes through this one function.
func SafeName(name string) string {
	var out strings.Builder

	for i := range len(name) {
		b := name[i]

		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' {
			out.WriteByte(b)

			continue
		}

		fmt.Fprintf(&out, "_%02X", b)
	}

	return out.String()
}
