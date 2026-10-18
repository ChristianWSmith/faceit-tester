//go:build !windows

package main

// isElevated always returns true off Windows; unprivileged ICMP sockets are
// used when available.
func isElevated() bool { return true }
