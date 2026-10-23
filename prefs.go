package main

const (
	// prefsDisabledKey is the Preferences key holding the denylist of unchecked
	// server IDs.
	prefsDisabledKey = "disabled_servers"
	// prefsExpandedKey is the Preferences key holding the list of expanded
	// tree nodes (region names and "Region/City" paths).
	prefsExpandedKey = "expanded_nodes"
	// prefsUpdatesDisabledKey is the Preferences key set by the update
	// dialog's "Never" button; when true the startup update check is
	// skipped (a forced --force-update-check still runs it).
	prefsUpdatesDisabledKey = "updates_disabled"
)

// aggregate reports whether every child is checked (all) and whether at
// least one child is checked (some). An empty group is vacuously all-checked.
// Together these map onto a tri-state parent: checked when all, partial when
// some && !all, unchecked otherwise.
func aggregate(states []bool) (all, some bool) {
	all = true
	for _, s := range states {
		if s {
			some = true
		} else {
			all = false
		}
	}
	return
}

// CityState is a city's tri-state contribution to its region checkbox.
type CityState struct {
	All  bool
	Some bool
}

// aggregateCities combines per-city tri-states for a region checkbox. A
// fully-checked city contributes like a checked child; a partial city forces
// the region partial; an empty city list is vacuously all-checked.
func aggregateCities(cities []CityState) (all, some bool) {
	all = true
	for _, c := range cities {
		if c.All {
			some = true
			continue
		}
		all = false
		if c.Some {
			some = true
		}
	}
	return
}

// stateFromDisabled builds the server-ID → checked map for the active server
// set, defaulting every server to checked and clearing only IDs that both
// appear in disabled and exist in servers. Unknown disabled IDs (from a
// deleted override file, older versions, etc.) are silently ignored.
func stateFromDisabled(servers map[string][]Server, disabled []string) map[string]bool {
	state := make(map[string]bool)
	for _, regionServers := range servers {
		for _, s := range regionServers {
			state[s.ID()] = true
		}
	}
	for _, id := range disabled {
		if _, ok := state[id]; ok {
			state[id] = false
		}
	}
	return state
}

// disabledFromState rebuilds the persisted denylist from state, restricted
// to IDs in the active server set. Stale IDs are pruned automatically.
func disabledFromState(servers map[string][]Server, state map[string]bool) []string {
	var disabled []string
	for _, region := range regionNames(servers) {
		for _, s := range servers[region] {
			if !state[s.ID()] {
				disabled = append(disabled, s.ID())
			}
		}
	}
	return disabled
}

// regionNodeKey is the prefs key for an expanded region node.
func regionNodeKey(region string) string {
	return region
}

// cityNodeKey is the prefs key for an expanded city node. "/" cannot appear
// in region or city names in the embedded data; override authors should
// avoid it (unknown keys are ignored on load regardless).
func cityNodeKey(region, city string) string {
	return region + "/" + city
}

// expandedFromList builds the set of expanded tree nodes from the persisted
// list, keeping only keys present in the active server set. Stale keys (from
// a deleted override, older versions, etc.) are silently ignored; an
// empty/absent list means everything starts collapsed.
func expandedFromList(servers map[string][]Server, list []string) map[string]bool {
	valid := make(map[string]bool)
	for _, region := range regionNames(servers) {
		valid[regionNodeKey(region)] = true
		for _, group := range groupByCity(servers[region]) {
			valid[cityNodeKey(region, group.City)] = true
		}
	}
	expanded := make(map[string]bool)
	for _, key := range list {
		if valid[key] {
			expanded[key] = true
		}
	}
	return expanded
}

// expandedToList rebuilds the persisted node list from state, restricted to
// keys in the active server set, in stable region/city order.
func expandedToList(servers map[string][]Server, expanded map[string]bool) []string {
	var list []string
	for _, region := range regionNames(servers) {
		if expanded[regionNodeKey(region)] {
			list = append(list, regionNodeKey(region))
		}
		for _, group := range groupByCity(servers[region]) {
			key := cityNodeKey(region, group.City)
			if expanded[key] {
				list = append(list, key)
			}
		}
	}
	return list
}
