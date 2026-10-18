//go:build windows

package main

import "golang.org/x/sys/windows"

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
