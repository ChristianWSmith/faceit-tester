//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errElevationCancelled is returned when the user dismisses the UAC
// consent prompt. Sentinel lives here (build-tagged file), but the value
// is compared via errors.Is from cross-platform code.
var errElevationCancelled = errors.New("elevation cancelled by user")

// isElevated reports whether the current process token is elevated (run as
// administrator). Required on Windows for raw ICMP sockets.
func isElevated() bool {
	var token windows.Token
	process := windows.CurrentProcess()
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// elevateAndRestart relaunches the current executable with the runas verb
// (UAC consent prompt). On success the new elevated instance is starting
// and the caller should exit.
func elevateAndRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	// ShellExecute fails with ERROR_CANCELLED (1223) when the prompt is
	// dismissed; any other failure returns the Win32 error.
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errElevationCancelled
		}
		return err
	}
	return nil
}
