package main

import "fmt"

// semver is a strict major.minor.patch triple. Prerelease/build-metadata
// suffixes are rejected (the project only cuts plain X.Y.Z tags, and
// GitHub's /releases/latest already excludes prereleases).
type semver struct {
	major, minor, patch int
}

// parseSemver parses a strict "X.Y.Z" tag. Anything else (date tags like
// "10-23-2026", "dev", empty, suffixes) is not semver.
func parseSemver(s string) (semver, bool) {
	var v semver
	n, err := fmt.Sscanf(s, "%d.%d.%d", &v.major, &v.minor, &v.patch)
	if err != nil || n != 3 {
		return semver{}, false
	}
	// Sscanf accepts "1.2.3.4" partial matches and negative numbers; re-render
	// the parsed triple and require an exact round-trip to reject them.
	if fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) != s {
		return semver{}, false
	}
	if v.major < 0 || v.minor < 0 || v.patch < 0 {
		return semver{}, false
	}
	return v, true
}

// cmp returns -1, 0, or 1 for v < other, v == other, v > other.
func (v semver) cmp(other semver) int {
	switch {
	case v.major != other.major:
		return cmpInt(v.major, other.major)
	case v.minor != other.minor:
		return cmpInt(v.minor, other.minor)
	default:
		return cmpInt(v.patch, other.patch)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// updateAvailable reports whether a build stamped with current should offer
// an update to the release tagged latest. Semver unconditionally outranks
// every non-semver tag so the retired date format ("10-23-2026") can never
// out-rank a semver build in either direction. "dev" (unstamped local or
// branch builds) never prompts. Unparsable latest, empty current, or an
// equal/older latest are all no-prompt.
func updateAvailable(current, latest string) bool {
	if current == "" || current == "dev" {
		return false
	}
	cur, curOK := parseSemver(current)
	lat, latOK := parseSemver(latest)
	switch {
	case latOK && curOK:
		return lat.cmp(cur) > 0
	case latOK && !curOK:
		// Legacy-stamped build (date tag): any semver release supersedes it.
		return true
	case !latOK && curOK:
		// Latest is a legacy date tag; the running semver build is newer.
		return false
	default:
		return false
	}
}
