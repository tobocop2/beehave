package sample

import "io"

// Outer embeds an interface of another package.
type Outer interface {
	io.Reader
	Gamma()
}
