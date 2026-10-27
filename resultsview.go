package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	// minNameColWidth is the floor for the Server column; it grows to fit
	// the widest name cell currently rendered (deep tree labels).
	minNameColWidth = 170
	// metricColWidth is the fixed width of the metric columns; sized for
	// the widest header ("Ping (med)" ≈ 83px padded) with slack for
	// platform font metrics.
	metricColWidth = float32(90)
)

// paneColumn labels one results-table column and the sort mode its
// header button cycles.
type paneColumn struct {
	label string
	mode  sortMode
}

// resultsColumns is the results table's layout — the full six-metric
// set, shared by the main window and the party summary (result codes
// carry every metric).
var resultsColumns = []paneColumn{
	// "Ping" is the gamer term for RTT and implies milliseconds; the
	// suffix says which statistic of the reply samples it is.
	{"Server", sortName},
	{"Ping (p1)", sortWorstP1},
	{"Ping (med)", sortWorstMedian},
	{"Ping (p99)", sortWorstP99},
	{"Jitter", sortWorstJitter},
	{"Loss", sortWorstLoss},
}

// paneConfig parameterizes one resultsPane instance's tap wiring (the
// main view copies the row; the party view opens the per-player
// breakdown). Columns are fixed — both views render the same table.
type paneConfig struct {
	onLeaf   func(o ServerOutcome) // leaf-row tap
	onLegend func()                // nil = no "?" legend button
	unfocus  func()                // canvas Focus(nil) after table taps
}

// resultsPane is the reusable results table: a sort-toggle header row
// over a widget.Table, both driven by a resultsTree. The main window
// and the party dialog each own one; the tree model (results.go) stays
// shared.
type resultsPane struct {
	cfg       paneConfig
	tree      *resultsTree
	rows      [][]tableCell
	refs      []rowRef // parallel to rows: leaf outcomes / branch identity
	header    *fyne.Container
	table     *widget.Table
	nameW     float32 // current Server column width (0 = unset)
	reservedW float32 // floor from setTree's fully-expanded preview
}

func newResultsPane(cfg paneConfig) *resultsPane {
	p := &resultsPane{cfg: cfg, tree: newResultsTree()}
	p.header = container.NewHBox()
	p.table = widget.NewTable(
		func() (int, int) { return len(p.rows), len(resultsColumns) },
		func() fyne.CanvasObject { return widget.NewLabel("0000.0") },
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			label := cell.(*widget.Label)
			if id.Row < len(p.rows) && id.Col < len(p.rows[id.Row]) {
				c := p.rows[id.Row][id.Col]
				label.Importance = c.imp
				label.TextStyle = fyne.TextStyle{Bold: c.bold}
				label.SetText(c.text)
			} else {
				label.Importance = widget.MediumImportance
				label.TextStyle = fyne.TextStyle{}
				label.SetText("")
			}
		},
	)
	// Same widths the header row uses; col 0 is re-measured by
	// refreshLayout once rows exist.
	p.table.SetColumnWidth(0, minNameColWidth)
	for i := 1; i < len(resultsColumns); i++ {
		p.table.SetColumnWidth(i, metricColWidth)
	}
	// Tapping a region/city row toggles its children; tapping a leaf
	// hands the outcome to cfg.onLeaf (copy row / breakdown dialog).
	// Unselect immediately so no row stays highlighted, and unfocus:
	// Table.Tapped focuses the table, which would swallow canvas
	// bare-key shortcuts.
	p.table.OnSelected = func(id widget.TableCellID) {
		p.onRowTap(id)
		p.table.UnselectAll()
		if cfg.unfocus != nil {
			cfg.unfocus()
		}
	}
	p.rebuildHeader(minNameColWidth)
	return p
}

// content returns the pane for layout: header row pinned above the
// table (callers must stretch it — a VBox would collapse the table to
// its min height).
func (p *resultsPane) content() fyne.CanvasObject {
	return container.NewBorder(p.header, nil, nil, nil, p.table)
}

// setTree swaps in a tree (run start, server-list change, new party
// summary) and re-renders it — the tree may already be populated (the
// party summary hands over a finished one), so this must flatten, not
// just clear. The Server column is reserved against a fully-expanded
// preview up front, so revealing branches can't re-flow the table.
// Rebuilding the header also resets any sort direction arrows left over
// from the previous tree; nameW resets to 0 so refreshLayout re-syncs
// both the header and the table's first column to the reservation.
func (p *resultsPane) setTree(t *resultsTree) {
	p.tree = t
	p.reservedW = reservedNameWidth(t)
	p.nameW = 0
	p.rebuildHeader(minNameColWidth)
	p.reflatten()
}

// reservedNameWidth measures the Server column against a
// fully-expanded copy of the tree's shape: every branch flips open for
// one flatten, then the original expansion state is restored (UI
// thread, no re-entry — the measurement is synchronous).
func reservedNameWidth(t *resultsTree) float32 {
	if t == nil {
		return minNameColWidth
	}
	type cityFlag struct {
		c   *resultsCity
		was bool
	}
	regionFlags := make([]struct {
		r   *resultsRegion
		was bool
	}, 0, len(t.regions))
	var cityFlags []cityFlag
	for _, r := range t.regions {
		regionFlags = append(regionFlags, struct {
			r   *resultsRegion
			was bool
		}{r, r.expanded})
		r.expanded = true
		for _, c := range r.cities {
			cityFlags = append(cityFlags, cityFlag{c, c.expanded})
			c.expanded = true
		}
	}
	rows, _ := t.flattenWithRefs()
	for _, f := range regionFlags {
		f.r.expanded = f.was
	}
	for _, f := range cityFlags {
		f.c.expanded = f.was
	}
	return nameColumnWidth(rows)
}

// addOutcome appends one merged/finished probe to the tree and
// re-renders.
func (p *resultsPane) addOutcome(region string, o ServerOutcome) {
	p.tree.addOutcome(region, o)
	p.reflatten()
}

// reflatten re-renders the tree into rows and repaints.
func (p *resultsPane) reflatten() {
	p.rows, p.refs = p.tree.flattenWithRefs()
	p.refreshLayout()
}

// onRowTap dispatches a table row tap: branch rows toggle expansion
// (in-memory only), leaf rows go to cfg.onLeaf.
func (p *resultsPane) onRowTap(id widget.TableCellID) {
	if id.Row < 0 || id.Row >= len(p.refs) {
		return
	}
	ref := p.refs[id.Row]
	if !ref.leaf {
		if p.tree != nil && p.tree.toggleNode(ref) {
			p.reflatten()
		}
		return
	}
	if p.cfg.onLeaf != nil {
		p.cfg.onLeaf(ref.outcome)
	}
}

// rebuildHeader recreates the header labels as sort-toggle buttons,
// sizing the Server column to nameWidth so it stays aligned with the
// table's dynamic first column. An active sort shows its direction (▾
// descending/worst-first, ▴ ascending/best-first). A trailing "?" button
// opens the legend when cfg.onLegend is set; it sits after the aligned
// cells and does not affect column spacing.
func (p *resultsPane) rebuildHeader(nameWidth float32) {
	p.header.RemoveAll()
	for i, h := range resultsColumns {
		width := metricColWidth
		if i == 0 {
			width = nameWidth
		}
		label := h.label
		if p.tree != nil && p.tree.mode == h.mode {
			if p.tree.desc {
				label += " ▾"
			} else {
				label += " ▴"
			}
		}
		col := i
		btn := widget.NewButton(label, func() { p.toggleSort(col) })
		btn.Importance = widget.LowImportance
		btn.Alignment = widget.ButtonAlignLeading
		p.header.Add(container.NewGridWrap(fyne.NewSize(width, btn.MinSize().Height), btn))
	}
	// Spacer pins the legend to the row's right edge when the pane is
	// wider than the columns. It contributes nothing to MinSize (spacers
	// are skipped there), so the pane/window min is exactly the column
	// cells + the button — no resize-on-layout risk.
	if p.cfg.onLegend != nil {
		p.header.Add(layout.NewSpacer())
		legendBtn := widget.NewButtonWithIcon("", theme.QuestionIcon(), func() { p.cfg.onLegend() })
		legendBtn.Importance = widget.LowImportance
		p.header.Add(legendBtn)
	}
}

// toggleSort cycles one column through three states: inactive → first
// direction → opposite direction → inactive. First direction is
// descending (worst-first) for metric columns — the app's whole point
// is surfacing problem locations — and ascending for Server (A→Z).
// The sort ranks a region's cities by their worst-case aggregate;
// providers keep probe order and regions keep insertion (RegionOrder)
// order.
func (p *resultsPane) toggleSort(col int) {
	if p.tree == nil || col < 0 || col >= len(resultsColumns) {
		return
	}
	mode := resultsColumns[col].mode
	firstDesc := mode != sortName
	switch {
	case p.tree.mode != mode:
		p.tree.setSort(mode, firstDesc)
	case p.tree.desc == firstDesc:
		p.tree.setSort(mode, !p.tree.desc)
	default:
		p.tree.setSort(sortProbeOrder, false)
	}
	p.reflatten()
	p.rebuildHeader(p.nameW)
}

// refreshLayout re-measures the name column against p.rows (never below
// the setTree reservation), updates the header when it changed, and
// re-pushes the width to the table's first column — the header and the
// body must always agree, which is why the SetColumnWidth is
// unconditional (a "no change" short-circuit once left the table at its
// old width while the header reserved more). Call after every rows
// rebuild.
func (p *resultsPane) refreshLayout() {
	w := nameColumnWidth(p.rows)
	if w < p.reservedW {
		w = p.reservedW
	}
	if w != p.nameW {
		p.nameW = w
		p.rebuildHeader(w)
	}
	p.table.SetColumnWidth(0, p.nameW)
	p.table.Refresh()
}

// nameColumnWidth returns the width needed for the widest name cell in
// rows (bold parents measured bold), floored at minNameColWidth.
func nameColumnWidth(rows [][]tableCell) float32 {
	w := float32(minNameColWidth)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		tw := fyne.MeasureText(row[0].text, theme.TextSize(), fyne.TextStyle{Bold: row[0].bold})
		// Label padding on both sides plus a hair of slack.
		if cw := tw.Width + 2*theme.Padding() + 2; cw > w {
			w = cw
		}
	}
	return w
}
