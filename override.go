package main

import (
	"bytes"
	"reflect"

	"fyne.io/fyne/v2"
)

// The custom server list lives in the app preferences under a single key
// as the same JSON dumpServersJSON writes — no more servers.json file
// next to the executable. The editor (editor.go) is the only writer;
// load/save here are the whole persistence surface.

// loadServers returns the active server table: the custom list stored in
// prefs when one exists, otherwise the embedded built-ins. The returned
// table is always usable — on a malformed stored list it is the built-ins
// and the error tells the caller to surface the problem. Callers must
// treat the returned table as read-only (the editor clones before
// mutating; see cloneServers).
func loadServers(prefs fyne.Preferences) (map[string][]Server, error) {
	raw := prefs.String(prefsServersOverrideKey)
	if raw == "" {
		return embeddedServers, nil
	}
	override, err := parseServersJSON([]byte(raw))
	if err != nil {
		return embeddedServers, err
	}
	if override == nil {
		return embeddedServers, nil
	}
	return override, nil
}

// overrideActive reports whether a custom list is stored — even a broken
// one the user should be able to fix or drop in the editor.
func overrideActive(prefs fyne.Preferences) bool {
	return prefs.String(prefsServersOverrideKey) != ""
}

// saveOverride stores servers as the custom list. A table that matches
// the built-ins removes the key instead, so "custom list active" stays an
// exact signal and editing your way back to the built-ins fully drops it.
func saveOverride(prefs fyne.Preferences, servers map[string][]Server) error {
	if reflect.DeepEqual(servers, embeddedServers) {
		prefs.RemoveValue(prefsServersOverrideKey)
		return nil
	}
	var buf bytes.Buffer
	if err := dumpServersJSON(&buf, servers); err != nil {
		return err
	}
	prefs.SetString(prefsServersOverrideKey, buf.String())
	return nil
}

// dropOverride deletes the custom list, returning to the built-ins.
func dropOverride(prefs fyne.Preferences) {
	prefs.RemoveValue(prefsServersOverrideKey)
}

// cloneServers deep-copies a table's region slices so an editor working
// copy can mutate without touching the built-ins or the live table (both
// are shared references, never edited in place).
func cloneServers(servers map[string][]Server) map[string][]Server {
	clone := make(map[string][]Server, len(servers))
	for region, list := range servers {
		clone[region] = append([]Server(nil), list...)
	}
	return clone
}
