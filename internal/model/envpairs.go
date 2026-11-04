package model

import "slices"

// EnvPairs returns the environment as NAME=value pairs, in sorted order.
func EnvPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for name, value := range env {
		pairs = append(pairs, name+"="+value)
	}

	slices.Sort(pairs)

	return pairs
}
