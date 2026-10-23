//go:build !windows && !linux && !darwin

package main

import "fmt"

// replaceAndRelaunch is a stub: platforms without a shipped release asset
// never reach the update flow (shouldCheckUpdate returns false), so there
// is nothing to self-replace.
func replaceAndRelaunch(stageDir, newBinaryName string) error {
	return fmt.Errorf("self-update is not supported on this platform")
}
