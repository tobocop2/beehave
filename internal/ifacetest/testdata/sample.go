package sample

// Inner is embedded by Outer.
type Inner interface {
	Alpha()
	Beta(x int) error
}

// Helper is a function, which holds no interface.
func Helper() {}

// Outer holds Inner and one method.
type Outer interface {
	Inner
	Gamma()
}
