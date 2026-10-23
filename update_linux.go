//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// replaceAndRelaunch swaps the running executable for the new one staged in
// stageDir and execs it in place (same PID, no orphan parent). Linux allows
// renaming a running executable; the old file becomes "<exe>.old" and is
// deleted by cleanupOlds in the new image.
func replaceAndRelaunch(stageDir, newBinaryName string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	staged := filepath.Join(stageDir, newBinaryName)
	if err := setExecutable(staged); err != nil {
		return fmt.Errorf("mark staged update executable: %w", err)
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move running executable aside: %w", err)
	}
	if err := copyFile(staged, exe); err != nil {
		os.Rename(old, exe)
		return fmt.Errorf("write new executable: %w", err)
	}
	argv := append([]string{exe}, os.Args[1:]...)
	return syscall.Exec(exe, argv, os.Environ())
}
