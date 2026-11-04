package model

// GateSpec is one step of a gate list: a name and the command line that runs it.
type GateSpec struct {
	Name    string
	Command string
}
