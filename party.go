package main

import (
	"fmt"
	"strconv"
	"strings"
)

// unknownRegion groups hosts the ranker's own server table doesn't
// know (a teammate's override target they haven't added): still ranked
// and joined by host, just without the ranker's naming.
const unknownRegion = "Unknown"

// partyRun is one player's run as fed to the ranker, in rank order.
type partyRun struct {
	label   string // "my run", "teammate 2", … — display only
	entries []partyEntry
}

// partyEntry is one run's result for one server with display identity
// already resolved against the ranker's own table — decoded teammates'
// runs via resolvePartyEntries, the local run straight from its
// ServerOutcome. All runs agree on naming because they all resolve
// through the same table.
type partyEntry struct {
	Region   string
	City     string
	Provider string
	Host     string
	Result   PingResult
	Scrubbed bool
}

// partyStats is the coverage of a ranking: only servers present in
// EVERY run are ranked; anything else is excluded and counted so the UI
// can warn (players run partial selections or carry different custom
// lists).
type partyStats struct {
	runs     int
	ranked   int // servers present in every run
	excluded int // distinct servers missing from at least one run
	union    int // distinct servers across all runs
}

// partyMember is one player's raw result for a server, shown in the
// per-player breakdown behind a ranked row.
type partyMember struct {
	label   string
	outcome ServerOutcome
}

// hostKey is the join key across players: the ping target itself.
// City.Provider IDs are override-relative (two players' custom lists
// can name the same target differently), so only the host is ground
// truth. DNS is case-insensitive, so keys are lowercased.
func hostKey(host string) string {
	return strings.ToLower(host)
}

// resolvedName is one server's display identity in the ranker's own
// table.
type resolvedName struct {
	region, city, provider string
}

// resolverFromTable builds the host → display-identity map from the
// ranker's ACTIVE server table (embedded or custom override — the
// party leader's naming reality). Deterministic via regionNames order;
// first wins on duplicate hosts.
func resolverFromTable(servers map[string][]Server) map[string]resolvedName {
	names := make(map[string]resolvedName)
	for _, region := range regionNames(servers) {
		for _, s := range servers[region] {
			k := hostKey(s.Host)
			if _, dup := names[k]; !dup {
				names[k] = resolvedName{region: region, city: s.City, provider: s.Provider}
			}
		}
	}
	return names
}

// resolvePartyEntries maps decoded code entries onto the ranker's
// table: known hosts get the ranker's region/city/provider; unknown
// hosts land in the Unknown region named by the target itself.
func resolvePartyEntries(entries []decodedEntry, names map[string]resolvedName) []partyEntry {
	out := make([]partyEntry, 0, len(entries))
	for _, e := range entries {
		pe := partyEntry{
			Host:     e.Host,
			Result:   e.Result,
			Scrubbed: e.Scrubbed,
			Region:   unknownRegion,
			City:     e.Host, // display by target until a name exists
		}
		if n, ok := names[hostKey(e.Host)]; ok {
			pe.Region, pe.City, pe.Provider = n.region, n.city, n.provider
		}
		out = append(out, pe)
	}
	return out
}

// stripSpace removes pasted line wraps and stray whitespace so a code
// that broke across messages still decodes.
func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

// parseResultCode is the slot-level decoder: pasted codes pick up line
// wraps and stray content (a Copy Summary dump, chat preamble), so
// resultCode first tries the whole text whitespace-stripped, then
// rejoins the code-like tokens. Empty input is a decode failure like
// any other (errNotResultCode).
func parseResultCode(text string) ([]decodedEntry, error) {
	_, entries, ok := resultCode(text)
	if !ok {
		return nil, errNotResultCode
	}
	return entries, nil
}

// minCodeTokenLen is the shortest rejoinable token: real code lines
// wrap at ~74 chars (or run as one long line), while summary words,
// numbers and host fragments are far shorter.
const minCodeTokenLen = 16

// resultCode extracts and decodes one code out of arbitrary pasted
// text, returning the extracted code itself (the dedup key for slots —
// the same paste wrapped differently must still register as a
// duplicate). Fast path: the whole text with whitespace stripped (the
// common clean paste, including codes that wrapped across lines). On
// failure — e.g. the paste also carries a Copy Summary dump — try the
// token reassemblies (joinCodeCandidates).
func resultCode(text string) (code string, entries []decodedEntry, ok bool) {
	if s := stripSpace(text); s != "" {
		if out, err := decodeResultCode(s); err == nil {
			return s, out, true
		}
	}
	for _, cand := range joinCodeCandidates(text) {
		if out, err := decodeResultCode(cand); err == nil {
			return cand, out, true
		}
	}
	return "", nil, false
}

// joinCodeCandidates returns the code reassembly candidates for text:
// the concatenation of its base64url tokens of at least
// minCodeTokenLen, plus that concatenation with the next short token
// appended — a code wrapped at an arbitrary width ends in one short
// remainder line (818 chars at 74/line = eleven long lines + a 4-char
// tail), and that tail must survive while surrounding summary words
// and numbers must not.
func joinCodeCandidates(text string) []string {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_')
	})
	var b strings.Builder
	lastLong := -1
	for i, tok := range tokens {
		if len(tok) >= minCodeTokenLen {
			b.WriteString(tok)
			lastLong = i
		}
	}
	base := b.String()
	if base == "" {
		return nil
	}
	cands := []string{base}
	if lastLong+1 < len(tokens) {
		cands = append(cands, base+tokens[lastLong+1])
	}
	return cands
}

// parsePartySlots validates the teammate input slots against the
// ranker's own server names: blank slots are skipped silently, a
// duplicate of an earlier slot is dropped with a note, an unparseable
// one is reported by its 1-based slot number. Runs come back in slot
// order (resolved through names) so breakdown labels match what the
// user pasted.
func parsePartySlots(slots []string, names map[string]resolvedName) (runs []partyRun, notes []string) {
	seen := map[string]int{} // extracted code → first slot
	for i, text := range slots {
		code, entries, ok := resultCode(text)
		if !ok {
			if stripSpace(text) != "" {
				slot := i + 1
				notes = append(notes, "Slot "+strconv.Itoa(slot)+": not a valid result code.")
			}
			continue
		}
		slot := i + 1
		if first, dup := seen[code]; dup {
			notes = append(notes, "Slot "+strconv.Itoa(slot)+" duplicates slot "+strconv.Itoa(first)+" — ignored.")
			continue
		}
		seen[code] = slot
		runs = append(runs, partyRun{
			label:   "teammate " + strconv.Itoa(slot),
			entries: resolvePartyEntries(entries, names),
		})
	}
	return runs, notes
}

// partyCoverageLine renders the Summarize result under the slots: how
// many runs fed the summary, how many servers were in every one, and
// what got dropped. stats.ranked == 0 means the intersection is empty.
func partyCoverageLine(stats partyStats) string {
	if stats.ranked == 0 {
		return "No servers in common across these result codes."
	}
	line := fmt.Sprintf("%d run%s · %d servers in common",
		stats.runs, pluralS(stats.runs), stats.ranked)
	if stats.excluded > 0 {
		line += fmt.Sprintf(" · %d excluded (not in every run)", stats.excluded)
	}
	return line
}

// pluralS is the tiny grammar helper the coverage line needs.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// partyRunFromOutcomes wraps the local player's own results (no code
// round-trip needed) as one more run for the ranker.
func partyRunFromOutcomes(label string, regionOf func(Server) string, outcomes []ServerOutcome) partyRun {
	entries := make([]partyEntry, 0, len(outcomes))
	for _, o := range outcomes {
		entries = append(entries, partyEntry{
			Region:   regionOf(o.Server),
			City:     o.Server.City,
			Provider: o.Server.Provider,
			Host:     o.Server.Host,
			Result:   o.Result,
			Scrubbed: o.Server.Scrubbed,
		})
	}
	return partyRun{label: label, entries: entries}
}

// buildParty intersects the runs by host and renders one merged leaf
// per shared server: column-wise worst across every player
// (mergeOutcomes), identity/region from the first run (all runs contain
// every ranked server — and all resolve through the same table, so the
// identities agree). Leaf insertion follows the first run's order,
// which is RegionOrder-first probe order — the tree then groups and
// sorts exactly like the main results view.
//
// members is keyed by hostKey for the per-player breakdown dialog.
func buildParty(runs []partyRun) (*resultsTree, partyStats, map[string][]partyMember) {
	stats := partyStats{runs: len(runs)}

	// Per-run first-wins maps (decode already dedups within one code,
	// but a local run shares this path un-deduped).
	perRun := make([]map[string]partyEntry, len(runs))
	union := map[string]bool{}
	for i, run := range runs {
		m := make(map[string]partyEntry, len(run.entries))
		for _, e := range run.entries {
			k := hostKey(e.Host)
			if _, dup := m[k]; !dup {
				m[k] = e
			}
			union[k] = true
		}
		perRun[i] = m
	}
	stats.union = len(union)
	if len(runs) == 0 {
		return newResultsTree(), stats, nil
	}

	// Intersection: keys present in every run.
	commonSet := map[string]bool{}
	for k := range union {
		inAll := true
		for _, m := range perRun {
			if _, ok := m[k]; !ok {
				inAll = false
				break
			}
		}
		if inAll {
			commonSet[k] = true
		}
	}
	stats.ranked = len(commonSet)
	stats.excluded = stats.union - stats.ranked

	members := make(map[string][]partyMember, len(commonSet))
	group := make([]ServerOutcome, 0, len(runs))
	tree := newResultsTree()
	// Run 0 drives ordering (probe order) and display identity.
	for _, e := range runs[0].entries {
		k := hostKey(e.Host)
		if !commonSet[k] {
			continue
		}
		if _, done := members[k]; done {
			continue // duplicate host inside run 0: first wins
		}
		group = group[:0]
		for i, m := range perRun {
			got := m[k]
			o := partyOutcome(got)
			group = append(group, o)
			members[k] = append(members[k], partyMember{label: runs[i].label, outcome: o})
		}
		merged, measurable := mergeOutcomes(group)
		leaf := partyOutcome(e)
		leaf.Result = merged
		// Render contract shared with worstCase: n/a loss/jitter exactly
		// when no run measured them.
		leaf.Server.Scrubbed = !measurable
		tree.addOutcome(e.Region, leaf)
	}
	return tree, stats, members
}

// partyOutcome lifts a resolved entry into the shared results model.
func partyOutcome(e partyEntry) ServerOutcome {
	return ServerOutcome{
		Server: Server{
			City:     e.City,
			Provider: e.Provider,
			Host:     e.Host,
			Scrubbed: e.Scrubbed,
		},
		Result: e.Result,
	}
}
