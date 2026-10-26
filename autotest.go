package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// errNoMatch is the sentinel from matchName when no candidate equals the
// want string case-insensitively; callers turn it into a contextual error.
var errNoMatch = errors.New("no matching name")

// parseTestSpec resolves a --test JSON spec against the loaded server table
// into a full server-ID → checked state map (unmatched entries false), the
// same shape stateFromDisabled produces. Grammar:
//
//	spec    := object                      // keys: region name or "all"
//	value   := "all" | cityObject
//	cityObj := object                      // keys: city name or "all"
//	cityVal := "all" | provider | [provider, ...]
//
// Names match case-insensitively; "all" is reserved at every level; unknown
// region/city/provider names and specs that select nothing are errors. The
// returned map is complete (every server ID present) so callers can use it
// as u.state directly.
func parseTestSpec(servers map[string][]Server, raw string) (map[string]bool, error) {
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	top, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New(`spec must be a JSON object, e.g. {"NA":"all"}`)
	}

	state := make(map[string]bool)
	regions := regionNames(servers)
	for _, region := range regions {
		for _, s := range servers[region] {
			state[s.ID()] = false
		}
	}

	for _, key := range sortedKeys(top) {
		// The "all" region key covers every region in the loaded table
		// (including override-only extras) and only takes the value "all".
		if strings.EqualFold(key, "all") {
			v, isStr := top[key].(string)
			if !isStr || !strings.EqualFold(v, "all") {
				return nil, errors.New(`the "all" region key only takes the value "all"`)
			}
			for _, region := range regions {
				for _, s := range servers[region] {
					state[s.ID()] = true
				}
			}
			continue
		}

		region, err := matchName(key, regions)
		if errors.Is(err, errNoMatch) {
			return nil, fmt.Errorf("unknown region %q (known: %s)", key, strings.Join(regions, ", "))
		}
		if err != nil {
			return nil, err
		}

		switch v := top[key].(type) {
		case string:
			if !strings.EqualFold(v, "all") {
				return nil, fmt.Errorf(`%s: region value must be "all" or a city object, got %q`, region, v)
			}
			for _, s := range servers[region] {
				state[s.ID()] = true
			}
		case map[string]any:
			if err := applyCityObject(region, servers[region], v, state); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(`%s: region value must be "all" or a city object`, region)
		}
	}

	selected := 0
	for _, ok := range state {
		if ok {
			selected++
		}
	}
	if selected == 0 {
		return nil, errors.New("spec selects no servers")
	}
	return state, nil
}

// applyCityObject expands one region's city object ({city: value}) into
// state selections. Cities and providers match case-insensitively; city
// values are "all", a provider name, or a list of provider names.
func applyCityObject(region string, srvs []Server, obj map[string]any, state map[string]bool) error {
	groups := groupByCity(srvs)
	cityNames := make([]string, len(groups))
	for i, g := range groups {
		cityNames[i] = g.City
	}

	for _, ckey := range sortedKeys(obj) {
		var targets []Server
		label := ckey
		if strings.EqualFold(ckey, "all") {
			for _, g := range groups {
				targets = append(targets, g.Servers...)
			}
			label = "all cities"
		} else {
			city, err := matchName(ckey, cityNames)
			if errors.Is(err, errNoMatch) {
				return fmt.Errorf("%s: unknown city %q (known: %s)", region, ckey, strings.Join(cityNames, ", "))
			}
			if err != nil {
				return err
			}
			label = city
			for _, g := range groups {
				if g.City == city {
					targets = append(targets, g.Servers...)
				}
			}
		}

		providers := providerNames(targets)
		switch cv := obj[ckey].(type) {
		case string:
			if strings.EqualFold(cv, "all") {
				markSelected(state, targets, "")
				continue
			}
			prov, err := matchName(cv, providers)
			if errors.Is(err, errNoMatch) {
				return fmt.Errorf("%s/%s: unknown provider %q (known: %s)", region, label, cv, strings.Join(providers, ", "))
			}
			if err != nil {
				return err
			}
			markSelected(state, targets, prov)
		case []any:
			if len(cv) == 0 {
				return fmt.Errorf("%s/%s: provider list is empty", region, label)
			}
			for _, item := range cv {
				name, isStr := item.(string)
				if !isStr {
					return fmt.Errorf("%s/%s: provider list entries must be strings", region, label)
				}
				prov, err := matchName(name, providers)
				if errors.Is(err, errNoMatch) {
					return fmt.Errorf("%s/%s: unknown provider %q (known: %s)", region, label, name, strings.Join(providers, ", "))
				}
				if err != nil {
					return err
				}
				markSelected(state, targets, prov)
			}
		default:
			return fmt.Errorf(`%s/%s: city value must be "all", a provider name, or a list of provider names`, region, label)
		}
	}
	return nil
}

// markSelected sets state true for every target server; with provider == ""
// all targets are selected, otherwise only those whose Provider matches
// case-insensitively.
func markSelected(state map[string]bool, targets []Server, provider string) {
	for _, s := range targets {
		if provider == "" || strings.EqualFold(s.Provider, provider) {
			state[s.ID()] = true
		}
	}
}

// matchName returns the candidate equal to want case-insensitively. It
// returns errNoMatch when nothing matches; names that collide only by case
// (impossible in well-formed tables) are reported as ambiguous.
func matchName(want string, candidates []string) (string, error) {
	var hit string
	for _, c := range candidates {
		if !strings.EqualFold(want, c) {
			continue
		}
		if hit != "" && hit != c {
			return "", fmt.Errorf("%q is ambiguous", want)
		}
		hit = c
	}
	if hit == "" {
		return "", errNoMatch
	}
	return hit, nil
}

// expansionForSelection returns the tree node keys that must be expanded to
// show every checked server's region. Only region nodes expand — cities stay
// collapsed because FACEIT players select a location, not a provider: the
// city checkbox's tri-state shows the selection, and the provider leaves
// underneath are a power-user detail. Callers merge the result into the
// persisted expansion map before the tree is built.
func expansionForSelection(servers map[string][]Server, state map[string]bool) map[string]bool {
	keys := make(map[string]bool)
	for _, region := range regionNames(servers) {
		for _, s := range servers[region] {
			if state[s.ID()] {
				keys[regionNodeKey(region)] = true
				break
			}
		}
	}
	return keys
}

// providerNames lists each server's Provider label, in order (duplicates
// across cities allowed — matchName tolerates identical repeats).
func providerNames(srvs []Server) []string {
	names := make([]string, len(srvs))
	for i, s := range srvs {
		names[i] = s.Provider
	}
	return names
}

// sortedKeys returns m's keys sorted, so multi-error specs report the same
// offender every time (map iteration order is random).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// quoteWinArg quotes s per Windows command-line rules (the CommandLineToArgvW
// convention): no quoting when free of spaces/quotes/tabs, otherwise wrapped
// in double quotes with interior backslashes before quotes (or the end)
// doubled and quotes escaped as \".
func quoteWinArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
			b.WriteByte(c)
		}
	}
	b.WriteString(strings.Repeat("\\", backslashes*2))
	b.WriteByte('"')
	return b.String()
}

// joinWinArgs renders args as a Windows command-line parameter string
// (each quoted, space-joined). Used as ShellExecute's lpParameters, which
// takes parameters only — the executable is the separate lpFile argument.
func joinWinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quoteWinArg(a)
	}
	return strings.Join(quoted, " ")
}
