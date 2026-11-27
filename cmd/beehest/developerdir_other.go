//go:build !darwin

package main

// developerFolder returns "" on a system with no developer folder of Apple.
func developerFolder([]string) string { return "" }
