//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// replaceAndRelaunch swaps the enclosing .app bundle for the new one staged
// in stageDir and spawns the new binary with the current arguments. The old
// bundle is renamed to "<bundle>.app.old" (allowed while its binary runs)
// and deleted by cleanupOlds on the next launch. A bare-binary run (not
// inside a .app) falls back to the linux-style single-file swap.
func replaceAndRelaunch(stageDir, newBinaryName string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if app := enclosingAppBundle(exe); app != "" {
		return replaceAppBundle(app, stageDir, exe)
	}
	return replaceBareBinary(exe, stageDir, newBinaryName)
}

// replaceAppBundle renames the running bundle aside, moves the freshly
// extracted bundle into its place, and spawns the new binary.
func replaceAppBundle(app, stageDir, oldExe string) error {
	stagedApp := findAppBundle(stageDir)
	if stagedApp == "" {
		return fmt.Errorf("staged update contains no .app bundle")
	}
	if err := setExecutable(filepath.Join(stagedApp, "Contents", "MacOS", filepath.Base(oldExe))); err != nil {
		return fmt.Errorf("mark staged binary executable: %w", err)
	}
	old := app + ".old"
	os.RemoveAll(old)
	if err := os.Rename(app, old); err != nil {
		return fmt.Errorf("move app bundle aside: %w", err)
	}
	if err := os.Rename(stagedApp, app); err != nil {
		os.Rename(old, app)
		return fmt.Errorf("install new app bundle: %w", err)
	}
	newExe := filepath.Join(app, "Contents", "MacOS", filepath.Base(oldExe))
	cmd := exec.Command(newExe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	return nil
}

// replaceBareBinary handles a run from a plain executable outside any .app
// bundle (e.g. a developer running `go build` output).
func replaceBareBinary(exe, stageDir, newBinaryName string) error {
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
	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	return nil
}
