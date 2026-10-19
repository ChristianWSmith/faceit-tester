package main

import (
	"fmt"
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

// toggle flips the expanded state of the branch shown at flat row, e.g.
// the row index flatten() would place it at. Returns false when the row
// is a leaf or does not exist.
func (t *resultsTree) toggle(row int) bool {
	idx := 0
	for _, r := range t.regions {
		if idx == row {
			r.expanded = !r.expanded
			return true
		}
		idx++
		if !r.expanded {
			continue
		}
		for _, c := range r.cities {
			if idx == row {
				c.expanded = !c.expanded
				return true
			}
			idx++
			if !c.expanded {
				continue
			}
			idx += len(c.providers)
		}
	}
	return false
}

// flatten renders the tree into results-table rows: bold aggregate rows
// for regions and cities (worst case across their finished children,
// with a ▸/▾ glyph and provider count), then indented leaf rows for each
// provider. Collapsed branches hide their descendants.
func (t *resultsTree) flatten() [][]tableCell {
	var rows [][]tableCell
	for _, r := range t.regions {
		var all []ServerOutcome
		for _, c := range r.cities {
			all = append(all, c.providers...)
		}
		rows = append(rows, worstCase(branchLabel(r.expanded, 0, r.name, len(all)), all))
		if !r.expanded {
			continue
		}
		for _, c := range r.cities {
			rows = append(rows, worstCase(branchLabel(c.expanded, 1, c.name, len(c.providers)), c.providers))
			if !c.expanded {
				continue
			}
			for _, o := range c.providers {
				leaf := outcomeRow(o)
				leaf[0].text = indentPrefix(2) + leaf[0].text
				rows = append(rows, leaf)
			}
		}
	}
	return rows
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
