package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
)

func codeEntry(region, city, provider, host string, res PingResult) partyEntry {
	return partyEntry{
		Region: region, City: city, Provider: provider, Host: host,
		Result: res,
	}
}

func ping(med, p99 time.Duration, loss float64) PingResult {
	return PingResult{Sent: 640, Recv: 640, LossPercent: loss, Median: med, P99: p99}
}

// testNames resolves the synthetic hosts the party tests share.
func testNames() map[string]resolvedName {
	return map[string]resolvedName{
		"server1.example.net":      {region: "NA", city: "San Jose", provider: "Dedicated.com"},
		"198.51.100.9":             {region: "NA", city: "Virginia", provider: "Psychz Networks"},
		"203.0.113.9":              {region: "EU", city: "Frankfurt", provider: "Hivelocity"},
		"192.0.2.88":               {region: "SA", city: "São Paulo", provider: "Latitude.sh"},
		"203.0.113.7":              {region: "NA", city: "San Jose", provider: "Dedicated.com"},
		"192.0.2.1":                {region: "NA", city: "Dallas", provider: "OVH"},
		"192.0.2.2":                {region: "NA", city: "Denver", provider: "Handy Networks"},
		"mirrors-lax.webnx.com":    {region: "NA", city: "San Jose", provider: "WebNX"},
		"speedtest.dal.hivelocity": {region: "NA", city: "Dallas", provider: "Hivelocity"},
	}
}

func TestResolverFromTable(t *testing.T) {
	servers := map[string][]Server{
		"NA": {
			{City: "Dallas", Provider: "OVH", Host: "192.0.2.1"},
			{City: "Chicago", Provider: "NForce", Host: "74.91.113.173", ProbeSize: 56},
		},
		"EU": {
			{City: "Frankfurt", Provider: "Hivelocity", Host: "example.net"},
		},
	}
	names := resolverFromTable(servers)
	if got := names["192.0.2.1"]; got != (resolvedName{region: "NA", city: "Dallas", provider: "OVH"}) {
		t.Errorf("192.0.2.1 → %+v, want NA/Dallas/OVH", got)
	}
	if got := names["example.net"]; got.region != "EU" {
		t.Errorf("example.net region = %q, want EU", got.region)
	}
	// Deterministic regardless of map iteration order (regionNames).
	if len(names) != 3 {
		t.Errorf("names = %d, want 3", len(names))
	}
}

func TestResolvePartyEntries(t *testing.T) {
	entries := []decodedEntry{
		{Host: "192.0.2.1", Result: ping(20*time.Millisecond, 30*time.Millisecond, 0)},
		{Host: "custom.other.example", Result: ping(90*time.Millisecond, 120*time.Millisecond, 1), Scrubbed: true},
	}
	got := resolvePartyEntries(entries, testNames())

	known := got[0]
	if known.Region != "NA" || known.City != "Dallas" || known.Provider != "OVH" {
		t.Errorf("known = %+v, want the table's naming", known)
	}
	if known.Scrubbed {
		t.Error("known host marked scrubbed")
	}
	unknown := got[1]
	if unknown.Region != unknownRegion || unknown.City != "custom.other.example" || unknown.Provider != "" {
		t.Errorf("unknown = %+v, want Unknown/%s/''", unknown, unknown.Region)
	}
	if !unknown.Scrubbed || unknown.Result.LossPercent != 1 {
		t.Errorf("unknown metrics lost: %+v", unknown)
	}
}

func TestParseResultCodeStripsWhitespace(t *testing.T) {
	code := encodeResultCode([]ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "OVH", Host: "192.0.2.1"},
			Result: ping(20*time.Millisecond, 30*time.Millisecond, 0)},
	})
	// Line wraps the way Discord/terminals break long tokens. (Codes are
	// short now; one wrap suffices — the messy-pastes test covers many.)
	wrapped := code[:20] + "\n  " + code[20:]
	out, err := parseResultCode(wrapped)
	if err != nil {
		t.Fatalf("parse wrapped code: %v", err)
	}
	if len(out) != 1 || out[0].Host != "192.0.2.1" {
		t.Errorf("parsed %+v, want the one Dallas entry", out)
	}
	if _, err := parseResultCode("   \n\t "); !errors.Is(err, errNotResultCode) {
		t.Errorf("empty input err = %v, want errNotResultCode", err)
	}
}

func TestParsePartySlots(t *testing.T) {
	mk := func(host string) string {
		return encodeResultCode([]ServerOutcome{
			{Server: Server{City: "X", Provider: "Y", Host: host},
				Result: ping(time.Millisecond, 2*time.Millisecond, 0)},
		})
	}
	a, b := mk("192.0.2.1"), mk("192.0.2.2")
	slots := []string{a, "   ", "garbage!!", a, b}
	runs, notes := parsePartySlots(slots, testNames())

	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (slot 1 + slot 5)", len(runs))
	}
	if runs[0].label != "teammate 1" || runs[1].label != "teammate 5" {
		t.Errorf("labels = %q, %q; want teammate 1, teammate 5", runs[0].label, runs[1].label)
	}
	// Resolution applied the ranker's names to the decoded hosts.
	if runs[0].entries[0].City != "Dallas" || runs[1].entries[0].City != "Denver" {
		t.Errorf("resolved cities = %q, %q; want Dallas, Denver", runs[0].entries[0].City, runs[1].entries[0].City)
	}
	if len(notes) != 2 {
		t.Fatalf("notes = %q, want 2 (invalid + duplicate)", notes)
	}
	if !strings.Contains(notes[0], "Slot 3") || !strings.Contains(notes[0], "not a valid result code") {
		t.Errorf("note 0 = %q, want slot 3 invalid", notes[0])
	}
	if !strings.Contains(notes[1], "Slot 4") || !strings.Contains(notes[1], "duplicates slot 1") {
		t.Errorf("note 1 = %q, want slot 4 duplicate of 1", notes[1])
	}

	// A duplicate of an INVALID slot is reported as invalid again, not
	// as a duplicate (only parsed codes enter the seen-set).
	_, notes = parsePartySlots([]string{"garbage!!", "garbage!!"}, testNames())
	if len(notes) != 2 || !strings.Contains(notes[1], "not a valid result code") {
		t.Errorf("invalid-dup notes = %q, want two invalid reports", notes)
	}
}

func TestBuildPartyIntersection(t *testing.T) {
	// Run 1: h1, h2 (NA) then h3 (EU, unshared). Run 2 shares h1, h2 and
	// brings its own h4 — and restates h1's host in a different case
	// (DNS is case-insensitive, so the join must be too).
	run1 := partyRun{label: "my run", entries: []partyEntry{
		codeEntry("NA", "San Jose", "Dedicated.com", "server1.example.net", ping(20*time.Millisecond, 30*time.Millisecond, 0)),
		codeEntry("NA", "Virginia", "Psychz Networks", "198.51.100.9", ping(25*time.Millisecond, 35*time.Millisecond, 1)),
		codeEntry("EU", "Frankfurt", "Hivelocity", "203.0.113.9", ping(90*time.Millisecond, 120*time.Millisecond, 2)),
	}}
	run2 := partyRun{label: "teammate 2", entries: []partyEntry{
		codeEntry("NA", "San Jose", "Dedicated.com", "SERVER1.EXAMPLE.NET", ping(21*time.Millisecond, 33*time.Millisecond, 0.5)),
		codeEntry("NA", "Virginia", "Psychz Networks", "198.51.100.9", ping(24*time.Millisecond, 40*time.Millisecond, 0)),
		codeEntry("SA", "São Paulo", "Latitude.sh", "192.0.2.88", ping(140*time.Millisecond, 200*time.Millisecond, 3)),
	}}

	tree, stats, members := buildParty([]partyRun{run1, run2})

	want := partyStats{runs: 2, union: 4, ranked: 2, excluded: 2}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if len(tree.regions) != 1 || tree.regions[0].name != "NA" {
		t.Fatalf("regions = %v, want only NA (EU/SA servers are unshared)", regionNames2(tree))
	}
	if len(tree.regions[0].cities) != 2 {
		t.Fatalf("cities = %d, want 2", len(tree.regions[0].cities))
	}
	// Leaf order follows run 1's probe order.
	if got := tree.regions[0].cities[0].name; got != "San Jose" {
		t.Errorf("first city = %q, want San Jose (run 1 order)", got)
	}
	if got := tree.regions[0].cities[1].name; got != "Virginia" {
		t.Errorf("second city = %q, want Virginia", got)
	}
	if len(members) != 2 {
		t.Errorf("members = %d keys, want 2", len(members))
	}
	if ms := members[hostKey("server1.example.net")]; len(ms) != 2 || ms[0].label != "my run" || ms[1].label != "teammate 2" {
		t.Errorf("members for h1 = %+v, want both runs in order", ms)
	}
}

func regionNames2(t *resultsTree) []string {
	var names []string
	for _, r := range t.regions {
		names = append(names, r.name)
	}
	return names
}

func TestBuildPartyUnknownRegion(t *testing.T) {
	// A teammate's override host the ranker's table lacks: still ranked,
	// grouped under Unknown, named by the target.
	run1 := partyRun{label: "my run", entries: []partyEntry{
		codeEntry(unknownRegion, "custom.example.net", "", "custom.example.net", ping(20*time.Millisecond, 30*time.Millisecond, 0)),
	}}
	run2 := partyRun{label: "teammate 2", entries: []partyEntry{
		codeEntry(unknownRegion, "custom.example.net", "", "custom.example.net", ping(25*time.Millisecond, 40*time.Millisecond, 0)),
	}}
	tree, stats, _ := buildParty([]partyRun{run1, run2})
	if stats.ranked != 1 {
		t.Fatalf("ranked = %d, want 1", stats.ranked)
	}
	if len(tree.regions) != 1 || tree.regions[0].name != unknownRegion {
		t.Fatalf("regions = %v, want [%s]", regionNames2(tree), unknownRegion)
	}
	leaf := tree.regions[0].cities[0].providers[0]
	if leaf.Server.DisplayName() != "custom.example.net" {
		t.Errorf("display = %q, want the bare host (no trailing dot)", leaf.Server.DisplayName())
	}
}

func TestBuildPartyMergeWorst(t *testing.T) {
	h := "203.0.113.7"
	mine := func(res PingResult, scrubbed bool) partyRun {
		e := codeEntry("NA", "San Jose", "Dedicated.com", h, res)
		e.Scrubbed = scrubbed
		return partyRun{label: "my run", entries: []partyEntry{e}}
	}

	t.Run("column-wise worst across players", func(t *testing.T) {
		// med from the slower player, loss from the lossier one.
		a := mine(ping(20*time.Millisecond, 50*time.Millisecond, 0), false)
		b := mine(ping(30*time.Millisecond, 40*time.Millisecond, 5), false)
		tree, _, _ := buildParty([]partyRun{a, b})
		leaf := tree.regions[0].cities[0].providers[0]
		if leaf.Result.Median != 30*time.Millisecond {
			t.Errorf("median = %v, want 30ms (worst player)", leaf.Result.Median)
		}
		if leaf.Result.P99 != 50*time.Millisecond {
			t.Errorf("p99 = %v, want 50ms (worst player)", leaf.Result.P99)
		}
		if leaf.Result.LossPercent != 5 {
			t.Errorf("loss = %v, want 5 (worst player)", leaf.Result.LossPercent)
		}
	})

	t.Run("one errored run skips to the others", func(t *testing.T) {
		ok := mine(ping(20*time.Millisecond, 44*time.Millisecond, 1), false)
		bad := partyRun{label: "teammate 3", entries: []partyEntry{
			codeEntry("NA", "San Jose", "Dedicated.com", h, PingResult{Err: errors.New("boom")}),
		}}
		tree, _, _ := buildParty([]partyRun{bad, ok})
		leaf := tree.regions[0].cities[0].providers[0]
		if leaf.Result.Err != nil {
			t.Fatalf("merged err = %v, want nil (a run succeeded)", leaf.Result.Err)
		}
		if leaf.Result.Median != 20*time.Millisecond || leaf.Result.LossPercent != 1 {
			t.Errorf("merged = %v/%v, want the successful run's values", leaf.Result.Median, leaf.Result.LossPercent)
		}
	})

	t.Run("all runs errored shows the error", func(t *testing.T) {
		e := func(msg string) partyRun {
			return partyRun{label: "x", entries: []partyEntry{
				codeEntry("NA", "San Jose", "Dedicated.com", h, PingResult{Err: errors.New(msg)}),
			}}
		}
		tree, _, _ := buildParty([]partyRun{e("first"), e("second")})
		leaf := tree.regions[0].cities[0].providers[0]
		if leaf.Result.Err == nil {
			t.Fatal("merged err = nil, want the first error")
		}
	})

	t.Run("scrubbed loss never wins", func(t *testing.T) {
		// The scrubbed run's loss is the scrubber's artifact — it must
		// not beat the measurable run's real loss.
		sc := mine(ping(15*time.Millisecond, 30*time.Millisecond, 99), true)
		real := mine(ping(18*time.Millisecond, 35*time.Millisecond, 2), false)
		tree, _, _ := buildParty([]partyRun{sc, real})
		leaf := tree.regions[0].cities[0].providers[0]
		if leaf.Result.LossPercent != 2 {
			t.Errorf("loss = %v, want 2 (measurable run only)", leaf.Result.LossPercent)
		}
		if leaf.Server.Scrubbed {
			t.Error("leaf marked scrubbed; one run measured the path")
		}
	})

	t.Run("all scrubbed renders n/a", func(t *testing.T) {
		sc1 := mine(ping(15*time.Millisecond, 30*time.Millisecond, 0), true)
		sc2 := mine(ping(16*time.Millisecond, 31*time.Millisecond, 0), true)
		tree, _, _ := buildParty([]partyRun{sc1, sc2})
		leaf := tree.regions[0].cities[0].providers[0]
		if !leaf.Server.Scrubbed {
			t.Error("leaf not marked scrubbed; no run measured loss")
		}
	})
}

func TestPartyRunFromOutcomesRegionWiring(t *testing.T) {
	outcomes := []ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "OVH", Host: "192.0.2.1"}, Result: ping(time.Millisecond, 2*time.Millisecond, 0)},
	}
	regionOf := func(s Server) string { return "NA" }
	run := partyRunFromOutcomes("my run", regionOf, outcomes)
	tree, stats, _ := buildParty([]partyRun{run})
	if stats.ranked != 1 {
		t.Errorf("ranked = %d, want 1", stats.ranked)
	}
	if len(tree.regions) != 1 || tree.regions[0].name != "NA" {
		t.Errorf("regions = %v, want [NA] from regionOf", regionNames2(tree))
	}
}

func TestPartyCoverageLine(t *testing.T) {
	if got := partyCoverageLine(partyStats{runs: 3, union: 66, ranked: 61, excluded: 5}); got !=
		"3 runs · 61 servers in common · 5 excluded (not in every run)" {
		t.Errorf("full = %q", got)
	}
	if got := partyCoverageLine(partyStats{runs: 3, union: 61, ranked: 61}); got !=
		"3 runs · 61 servers in common" {
		t.Errorf("no exclusions = %q", got)
	}
	if got := partyCoverageLine(partyStats{runs: 1, union: 66, ranked: 66}); got !=
		"1 run · 66 servers in common" {
		t.Errorf("single run = %q", got)
	}
	if got := partyCoverageLine(partyStats{runs: 2, union: 5, ranked: 0, excluded: 5}); got !=
		"No servers in common across these result codes." {
		t.Errorf("empty intersection = %q", got)
	}
}

// TestParseResultCodeFromMessyPastes pins the extraction path with the
// shapes users actually paste: a code followed by a Copy Summary dump
// (run1.txt/run2.txt in the repo history — code on line 1, summary
// after), and a code wrapped at ~74 columns with trailing junk. The
// rejoin must also normalize wrapping so the same code pasted twice
// still registers as a slot duplicate.
func TestParseResultCodeFromMessyPastes(t *testing.T) {
	code := encodeResultCode([]ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "OVH", Host: "192.0.2.1"},
			Result: ping(20*time.Millisecond, 30*time.Millisecond, 0.31)},
	})
	summary := "FACEIT tester — Oct 26, 13:58\n\n" +
		"Server                                    med      p99   jitter    loss\n" +
		"Dallas.OVH                              20.0ms    30.0ms     1.0ms   0.31%\n"

	// Whole paste = code + summary dump.
	out, err := parseResultCode(code + "\n\n" + summary)
	if err != nil || len(out) != 1 || out[0].Host != "192.0.2.1" {
		t.Fatalf("code+summary parse = (%v, %v), want the Dallas entry", out, err)
	}

	// Code wrapped every 74 chars, then junk — each wrapped line is a
	// code-like token (with a short remainder line); the summary words
	// are not.
	rest := code
	var wrapped []string
	for len(rest) > 0 {
		n := 74
		if n > len(rest) {
			n = len(rest)
		}
		wrapped = append(wrapped, rest[:n])
		rest = rest[n:]
	}
	out, err = parseResultCode(strings.Join(wrapped, "\n") + "\n" + summary)
	if err != nil || len(out) != 1 {
		t.Fatalf("wrapped+junk parse = (%v, %v), want the Dallas entry", out, err)
	}

	// The same content pasted two ways is still one code: slot dedup
	// keys on the extracted code, not the raw text.
	runs, notes := parsePartySlots(
		[]string{code + "\n\n" + summary, strings.Join(wrapped, "\r\n")}, testNames())
	if len(runs) != 1 || len(notes) != 1 || !strings.Contains(notes[0], "duplicates") {
		t.Errorf("dedup across wrappings: runs=%d notes=%v, want 1 run + duplicate note", len(runs), notes)
	}
}

// TestPartyTreeRendersInPane is the integration pin: a party tree fed
// through the shared pane with the main column config renders full
// 6-cell rows carrying the cross-player worst values.
func TestPartyTreeRendersInPane(t *testing.T) {
	test.NewApp()
	run1 := partyRun{label: "my run", entries: []partyEntry{
		codeEntry("NA", "San Jose", "Dedicated.com", "203.0.113.7",
			ping(20100*time.Microsecond, 40200*time.Microsecond, 0.31)),
	}}
	run1.entries[0].Result.P1 = 15000 * time.Microsecond
	run1.entries[0].Result.Jitter = 2500 * time.Microsecond
	run2 := partyRun{label: "teammate 2", entries: []partyEntry{
		codeEntry("NA", "San Jose", "Dedicated.com", "203.0.113.7",
			ping(30500*time.Microsecond, 35000*time.Microsecond, 5)),
	}}
	run2.entries[0].Result.P1 = 28000 * time.Microsecond
	run2.entries[0].Result.Jitter = 9000 * time.Microsecond
	tree, _, _ := buildParty([]partyRun{run1, run2})

	p := newResultsPane(paneConfig{})
	p.setTree(tree)
	// setTree must flatten the pre-populated tree itself (the dialog's
	// Rank path never calls reflatten) — cities start collapsed, so the
	// region row is already there before any expansion.
	if len(p.rows) != 2 {
		t.Fatalf("rows right after setTree = %d, want 2 (region + collapsed city)", len(p.rows))
	}
	p.tree.regions[0].cities[0].expanded = true
	p.reflatten()

	if len(p.rows) != 3 {
		t.Fatalf("rows = %d, want 3 (region + city + leaf)", len(p.rows))
	}
	leaf := p.rows[2]
	if len(leaf) != 6 {
		t.Fatalf("leaf cells = %d, want 6 (full column set)", len(leaf))
	}
	if leaf[1].text != "28.0" {
		t.Errorf("p1 = %q, want 28.0 (worst player)", leaf[1].text)
	}
	if leaf[2].text != "30.5" {
		t.Errorf("med = %q, want 30.5 (worst player)", leaf[2].text)
	}
	if leaf[3].text != "40.2" {
		t.Errorf("p99 = %q, want 40.2 (worst player)", leaf[3].text)
	}
	if leaf[4].text != "9.0" {
		t.Errorf("jitter = %q, want 9.0 (worst player)", leaf[4].text)
	}
	if leaf[5].text != "5.00%" {
		t.Errorf("loss = %q, want 5.00%% (worst player)", leaf[5].text)
	}
}
