//go:build !windows

package main

import "errors"

var errElevationCancelled = errors.New("elevation cancelled by user")

// isElevated always returns true off Windows; unprivileged ICMP sockets are
// used when available.
func isElevated() bool { return true }

// elevateAndRestart is a no-op off Windows; isElevated() is always true
// there so the button never appears.
func elevateAndRestart() error { return nil }
