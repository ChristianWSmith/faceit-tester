//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// replaceAndRelaunch swaps the running executable for the new one staged in
// stageDir and starts it with the current arguments. Windows can't overwrite
// a running exe, so the old file is renamed to "<exe>.old" first (allowed
// while running); the relaunched process deletes it via cleanupOlds. The
// child inherits this process's elevation, so there's no second UAC prompt.
func replaceAndRelaunch(stageDir, newBinaryName string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	staged := filepath.Join(stageDir, newBinaryName)
	if _, err := os.Stat(staged); err != nil {
		return fmt.Errorf("staged update missing: %w", err)
	}
	old := exe + ".old"
	os.Remove(old) // best-effort: clear a leftover from a prior update
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move running executable aside: %w", err)
	}
	if err := copyFile(staged, exe); err != nil {
		// Put the old binary back so the user isn't left without an app.
		os.Rename(old, exe)
		return fmt.Errorf("write new executable: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	return nil
}
