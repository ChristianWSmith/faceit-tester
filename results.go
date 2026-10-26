package main

import (
	"fmt"
	"sort"
	"strings"
)

// resultsTree is the hierarchical results model behind the results table:
// region → city → provider, mirroring the selection tree's grouping.
// Branches are created on first outcome (regions expanded, cities
// collapsed); the table rows are produced by flatten(), which recomputes
// each branch's worst-case aggregate on every render.
type resultsTree struct {
	regions []*resultsRegion
	byName  map[string]*resultsRegion
	mode    sortMode
	desc    bool // direction for the active mode (meaningless at sortProbeOrder)
}

type resultsRegion struct {
	name     string
	expanded bool
	cities   []*resultsCity
	byName   map[string]*resultsCity
}

type resultsCity struct {
	name      string
	expanded  bool
	providers []ServerOutcome
}

// sortMode selects how a region's *cities* are ordered. The default
// preserves probe order; metric modes rank cities by their worst-case
// aggregate so problem locations surface without scanning the table.
// Cities are the unit players actually choose (locations, not
// providers), so leaves within a city always keep probe order.
type sortMode int

const (
	sortProbeOrder sortMode = iota
	sortWorstP1
	sortWorstMedian
	sortWorstP99
	sortWorstJitter
	sortWorstLoss
	sortName
)

// rowRef identifies what one flat table row points at: a branch (region
// or city, toggled on tap) or a leaf outcome (copied / error-detail on
// tap). Built alongside rows by flattenWithRefs so tap handlers never
// re-derive indexes from tree order.
type rowRef struct {
	leaf    bool
	region  int // region index in t.regions
	city    int // city index within the region (-1 for region rows)
	outcome ServerOutcome
}

func newResultsTree() *resultsTree {
	return &resultsTree{byName: map[string]*resultsRegion{}}
}

// addOutcome appends one finished probe to its region/city branch,
// creating the branches on first sight. New regions start expanded and
// new cities start collapsed (mirroring the left pane's collapsed
// default); children keep completion order, which equals probe/selection
// order.
func (t *resultsTree) addOutcome(region string, o ServerOutcome) {
	r, ok := t.byName[region]
	if !ok {
		r = &resultsRegion{
			name:     region,
			expanded: true,
			byName:   map[string]*resultsCity{},
		}
		t.byName[region] = r
		t.regions = append(t.regions, r)
	}
	c, ok := r.byName[o.Server.City]
	if !ok {
		c = &resultsCity{name: o.Server.City, expanded: false}
		r.byName[o.Server.City] = c
		r.cities = append(r.cities, c)
	}
	c.providers = append(c.providers, o)
}

// toggleNode flips the expanded state of the branch a rowRef points at.
// Returns false for leaf refs.
func (t *resultsTree) toggleNode(ref rowRef) bool {
	if ref.leaf || ref.region < 0 || ref.region >= len(t.regions) {
		return false
	}
	r := t.regions[ref.region]
	if ref.city < 0 {
		r.expanded = !r.expanded
		return true
	}
	if ref.city >= len(r.cities) {
		return false
	}
	r.cities[ref.city].expanded = !r.cities[ref.city].expanded
	return true
}

// setSort changes the sibling sort (mode + direction); re-rendering is
// the caller's job (they already re-flatten after any change).
func (t *resultsTree) setSort(m sortMode, desc bool) {
	t.mode, t.desc = m, desc
}

// flatten renders the tree into results-table rows (no refs; callers
// that need tap targets use flattenWithRefs).
func (t *resultsTree) flatten() [][]tableCell {
	rows, _ := t.flattenWithRefs()
	return rows
}

// flattenWithRefs renders the tree into results-table rows plus the
// parallel rowRefs slice: bold aggregate rows for regions and cities
// (worst case across their finished children, with a ▸/▾ glyph and
// provider count), then indented leaf rows for each provider. Collapsed
// branches hide their descendants. Cities are ordered per t.mode/t.desc
// (probe order by default); regions keep RegionOrder; providers keep
// probe order — the sort ranks locations, not providers. Parents always
// render before their children.
func (t *resultsTree) flattenWithRefs() ([][]tableCell, []rowRef) {
	var rows [][]tableCell
	var refs []rowRef
	add := func(cells []tableCell, ref rowRef) {
		rows = append(rows, cells)
		refs = append(refs, ref)
	}
	for ri, r := range t.regions {
		var all []ServerOutcome
		for _, c := range r.cities {
			all = append(all, c.providers...)
		}
		add(worstCase(branchLabel(r.expanded, 0, r.name, len(all)), all),
			rowRef{region: ri, city: -1})
		if !r.expanded {
			continue
		}
		for _, idx := range sortedCityIndexes(r, t.mode, t.desc) {
			c := r.cities[idx]
			add(worstCase(branchLabel(c.expanded, 1, c.name, len(c.providers)), c.providers),
				rowRef{region: ri, city: idx})
			if !c.expanded {
				continue
			}
			for _, o := range c.providers {
				leaf := outcomeRow(o)
				leaf[0].text = indentPrefix(2) + leaf[0].text
				add(leaf, rowRef{leaf: true, region: ri, city: idx, outcome: o})
			}
		}
	}
	return rows, refs
}

// sortedCityIndexes returns a region's city indexes in display order for
// mode/desc. Metric modes rank the cities' worst-case aggregates
// (descending = worst first; ascending = best first); sortName is
// alphabetical in the requested direction; sortProbeOrder keeps
// insertion order. Errors/n-a aggregates rank last in both directions.
// Indexes (not pointers) so rowRefs can address r.cities directly.
func sortedCityIndexes(r *resultsRegion, mode sortMode, desc bool) []int {
	idx := make([]int, len(r.cities))
	for i := range idx {
		idx[i] = i
	}
	if mode == sortProbeOrder {
		return idx
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := r.cities[idx[i]], r.cities[idx[j]]
		if mode == sortName {
			la, lb := strings.ToLower(a.name), strings.ToLower(b.name)
			if desc {
				return la > lb
			}
			return la < lb
		}
		return aggregateCellLess(mode, desc, worstCase("", a.providers), worstCase("", b.providers))
	})
	return idx
}

// aggregateCellLess compares two worstCase rows on the metric column for
// mode/direction by parsing their formatted cells (rowCellLess).
func aggregateCellLess(mode sortMode, desc bool, a, b []tableCell) bool {
	return rowCellLess(mode, desc, a, b)
}

// rowCellLess compares two worstCase-produced rows on the metric column
// for mode/direction, parsing the formatted cell text. Unparseable
// ("—"/"n/a"/"error") cells sort last in BOTH directions — an
// unmeasurable city is not "best" because it has no number.
func rowCellLess(mode sortMode, desc bool, a, b []tableCell) bool {
	col := map[sortMode]int{
		sortWorstP1: 1, sortWorstMedian: 2, sortWorstP99: 3,
		sortWorstJitter: 4, sortWorstLoss: 5,
	}[mode]
	if col == 0 {
		return false
	}
	av, aok := parseCellMetric(a[col])
	bv, bok := parseCellMetric(b[col])
	if aok != bok {
		return aok
	}
	if !aok {
		return false
	}
	if desc {
		return av > bv
	}
	return av < bv
}

// parseCellMetric reads a numeric metric out of a formatted cell:
// "28.4" (ms columns) or "1.60" (loss %). "—", "n/a", "error" fail.
func parseCellMetric(c tableCell) (float64, bool) {
	t := strings.TrimSpace(c.text)
	if t == "" || t == "—" || t == "n/a" || strings.HasPrefix(t, "error") {
		return 0, false
	}
	t = strings.TrimSuffix(t, "ms")
	t = strings.TrimSuffix(t, "%")
	t = strings.TrimSpace(t)
	var v float64
	if _, err := fmt.Sscanf(t, "%g", &v); err != nil {
		return 0, false
	}
	return v, true
}

// branchLabel renders a parent row's name cell: expander glyph, depth
// indent, name, and child count (matching the selection tree's style).
func branchLabel(expanded bool, depth int, name string, count int) string {
	glyph := "▸"
	if expanded {
		glyph = "▾"
	}
	return fmt.Sprintf("%s%s %s (%d)", indentPrefix(depth), glyph, name, count)
}

// indentPrefix is the name-cell indent for one tree depth level (region 0,
// city 1, provider 2); four spaces per level so hierarchy reads at a glance
// in the proportional UI font.
func indentPrefix(depth int) string {
	return strings.Repeat("    ", depth)
}

// worstCase renders one branch aggregate row: column-by-column worst
// values across the branch's outcomes (players cannot choose their
// provider, so this is what they might actually get). Errored outcomes
// are skipped for the numeric columns; if every outcome errored, the
// first error is shown. Scrubbed outcomes contribute their RTT columns
// but never jitter/loss (those show n/a unless a measurable outcome
// exists). The name cell is bold.
func worstCase(name string, outcomes []ServerOutcome) []tableCell {
	var worst PingResult
	have := false
	haveMeasurable := false
	var firstErr error
	for _, o := range outcomes {
		r := o.Result
		if r.Err != nil {
			if firstErr == nil {
				firstErr = r.Err
			}
			continue
		}
		if !have {
			worst = r
			have = true
		} else {
			if r.Recv > worst.Recv {
				worst.Recv = r.Recv
			}
			if r.Median > worst.Median {
				worst.Median = r.Median
			}
			if r.P1 > worst.P1 {
				worst.P1 = r.P1
			}
			if r.P99 > worst.P99 {
				worst.P99 = r.P99
			}
		}
		if !o.Server.Scrubbed {
			if !haveMeasurable {
				// Seed jitter/loss from the first measurable outcome.
				worst.Jitter, worst.LossPercent = r.Jitter, r.LossPercent
				haveMeasurable = true
				continue
			}
			if r.Jitter > worst.Jitter {
				worst.Jitter = r.Jitter
			}
			if r.LossPercent > worst.LossPercent {
				worst.LossPercent = r.LossPercent
			}
		}
	}
	if !have {
		row := resultRow(name, PingResult{Err: firstErr}, false)
		row[0].bold = true
		return row
	}
	row := resultRow(name, worst, !haveMeasurable)
	row[0].bold = true
	return row
}
