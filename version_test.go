package main

import "testing"

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in   string
		want semver
		ok   bool
	}{
		{"0.1.0", semver{0, 1, 0}, true},
		{"1.2.3", semver{1, 2, 3}, true},
		{"10.20.30", semver{10, 20, 30}, true},
		{"0.10.0", semver{0, 10, 0}, true},
		{"dev", semver{}, false},
		{"", semver{}, false},
		{"10-23-2026", semver{}, false},
		{"1.2", semver{}, false},
		{"1.2.3.4", semver{}, false},
		{"1.2.3-rc.1", semver{}, false},
		{"v1.2.3", semver{}, false},
		{"-1.2.3", semver{}, false},
		{"01.2.3", semver{}, false},
	}
	for _, tt := range tests {
		got, ok := parseSemver(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parseSemver(%q) = %+v,%v want %+v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSemverCmp(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"0.9.0", "0.10.0", -1}, // numeric, not lexicographic
		{"1.0.0", "0.99.99", 1},
		{"1.2.3", "2.0.0", -1},
	}
	for _, tt := range tests {
		a, _ := parseSemver(tt.a)
		b, _ := parseSemver(tt.b)
		if got := a.cmp(b); got != tt.want {
			t.Errorf("%s cmp %s = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestUpdateAvailable(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", true},
		{"0.2.0", "0.1.0", false},
		{"0.9.0", "0.10.0", true},
		// dev / unstamped builds never prompt.
		{"dev", "0.2.0", false},
		{"", "0.2.0", false},
		// Semver beats legacy date tags in both directions.
		{"0.1.0", "10-23-2026", false},
		{"10-23-2026", "0.1.0", true},
		{"10-22-2026", "10-23-2026", false}, // neither is semver
		// Unparsable latest never prompts for a semver build.
		{"0.1.0", "garbage", false},
		{"0.1.0", "", false},
	}
	for _, tt := range tests {
		if got := updateAvailable(tt.current, tt.latest); got != tt.want {
			t.Errorf("updateAvailable(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}
