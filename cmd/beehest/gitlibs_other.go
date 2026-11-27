//go:build !darwin && !linux

package main

import (
	"context"
	"errors"
)

// gitLibraries is an error on a system with no sandbox enforcer, where the copy never runs.
func gitLibraries(context.Context, string) ([]string, error) {
	return nil, errors.New("this system has no sandbox enforcer, so the copy does not find the libraries of git")
}
