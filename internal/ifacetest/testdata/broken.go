package sample

// Outer embeds an interface that the file lacks.
type Outer interface {
	Gone
}

// Value is a type that is no interface.
type Value struct{}

// Limit is a constant.
const Limit = 1
