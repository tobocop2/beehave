//go:build !linux

package proc

// CapMeaning says in words what max_new_processes limits on this system. It is a format with the cap as its one number.
// This system has no counter for one tree, so a step with max_new_processes uses the per-user limit of ExecRunner.
const CapMeaning = "the process count of the user at the start plus %d, shared with the other programs of the user"
