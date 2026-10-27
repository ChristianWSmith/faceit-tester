package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestFormatETA(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{59 * time.Second, "59s"},
		{90 * time.Second, "2m"},
		{9 * time.Minute, "9m"},
		{59 * time.Minute, "59m"},
		{72 * time.Minute, "1h 12m"},
		{3*time.Hour + 5*time.Minute, "3h 5m"},
	}
	for _, tc := range cases {
		if got := formatETA(tc.d); got != tc.want {
			t.Errorf("formatETA(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRunningLoss(t *testing.T) {
	if _, show := runningLoss(10, 10); show {
		t.Error("runningLoss should suppress the figure below 32 sent")
	}
	if _, show := runningLoss(31, 30); show {
		t.Error("runningLoss should suppress at exactly 31 sent")
	}
	loss, show := runningLoss(640, 624)
	if !show {
		t.Fatal("runningLoss should show at 640 sent")
	}
	if loss != "2.5%" {
		t.Errorf("runningLoss = %q, want 2.5%%", loss)
	}
	loss, _ = runningLoss(640, 640)
	if loss != "0.0%" {
		t.Errorf("zero-loss = %q, want 0.0%%", loss)
	}
}

func TestVersionDisplay(t *testing.T) {
	// version is the package var; restore it after.
	old := version
	defer func() { version = old }()
	version = ""
	if got := versionDisplay(); got != "dev build" {
		t.Errorf("empty version = %q, want dev build", got)
	}
	version = "dev"
	if got := versionDisplay(); got != "dev build" {
		t.Errorf("dev version = %q, want dev build", got)
	}
	version = "1.2.3"
	if got := versionDisplay(); got != "1.2.3" {
		t.Errorf("release version = %q, want 1.2.3", got)
	}
}

func TestTruncateMarkdown(t *testing.T) {
	if got := truncateMarkdown("", 100); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	short := "## v1.0.1\n\n- Fix a bug"
	if got := truncateMarkdown(short, 100); got != short {
		t.Errorf("under limit unchanged, got %q", got)
	}
	long := strings.Repeat("word ", 200)
	got := truncateMarkdown(long, 60)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("over limit should end with ellipsis, got %q", got)
	}
	if len([]rune(strings.TrimSuffix(got, "…"))) > 60+len("word") {
		t.Errorf("cut too far past the limit: %q", got)
	}
}

func TestClampSplitOffset(t *testing.T) {
	if got := clampSplitOffset(0); got != 0.05 {
		t.Errorf("0 permille = %v, want clamp to 0.05", got)
	}
	if got := clampSplitOffset(350); got != 0.35 {
		t.Errorf("350 permille = %v, want 0.35", got)
	}
	if got := clampSplitOffset(1000); got != 0.95 {
		t.Errorf("1000 permille = %v, want clamp to 0.95", got)
	}
}

func TestFormatOutcomeLine(t *testing.T) {
	normal := ServerOutcome{
		Server: srv("Chicago", "Psychz Networks", "108.181.62.197"),
		Result: PingResult{
			Median: 22 * time.Millisecond, P99: 29 * time.Millisecond,
			Jitter: 1500 * time.Microsecond, LossPercent: 0.16,
		},
	}
	line := formatOutcomeLine(normal, "NA")
	for _, want := range []string{"NA", "Chicago", "Psychz Networks", "108.181.62.197", "22.0", "0.16%"} {
		if !strings.Contains(line, want) {
			t.Errorf("normal line %q missing %q", line, want)
		}
	}

	scrubbed := ServerOutcome{
		Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "h", Scrubbed: true},
		Result: PingResult{Median: 49 * time.Millisecond, P99: 55 * time.Millisecond},
	}
	line = formatOutcomeLine(scrubbed, "NA")
	if !strings.Contains(line, "icmp-scrubbed") || !strings.Contains(line, "n/a") {
		t.Errorf("scrubbed line = %q, want n/a + icmp-scrubbed", line)
	}

	errored := ServerOutcome{
		Server: srv("EU", "broken", "10.0.0.1"),
		Result: PingResult{Err: errors.New("network is unreachable")},
	}
	line = formatOutcomeLine(errored, "EU")
	if !strings.Contains(line, "error: network is unreachable") {
		t.Errorf("errored line = %q", line)
	}
}

func TestBuildSummaryText(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 32, 0, 0, time.UTC)
	outcomes := []ServerOutcome{
		{
			Server: srv("Chicago", "Psychz Networks", "a"),
			Result: PingResult{
				Median: 22 * time.Millisecond, P99: 29 * time.Millisecond,
				Jitter: 1500 * time.Microsecond, LossPercent: 0.16,
			},
		},
		{
			Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "h", Scrubbed: true},
			Result: PingResult{Median: 49 * time.Millisecond, P99: 55 * time.Millisecond},
		},
		{
			Server: srv("EU", "broken", "10.0.0.1"),
			Result: PingResult{Err: errors.New("boom")},
		},
	}
	text := buildSummaryText(now, outcomes, func(Server) string { return "NA" })
	if !strings.Contains(text, "Oct 25, 14:32") {
		t.Errorf("summary missing timestamp:\n%s", text)
	}
	if !strings.Contains(text, "Chicago.Psychz Networks") {
		t.Errorf("summary missing first outcome:\n%s", text)
	}
	if !strings.Contains(text, "n/a") {
		t.Errorf("summary missing scrubbed n/a:\n%s", text)
	}
	if !strings.Contains(text, "error: boom") {
		t.Errorf("summary missing error:\n%s", text)
	}
	// Three data lines + header + blank line + column header.
	if lines := strings.Count(text, "\n"); lines < 6 {
		t.Errorf("summary too short (%d lines):\n%s", lines, text)
	}
}

// TestSortedCityIndexes pins city-level sorting: metric modes rank the
// cities' worst-case aggregates in the requested direction, errors rank
// last in BOTH directions, sortName is alphabetical, and sortProbeOrder
// is identity. (Providers are never sorted — players pick locations.)
func TestSortedCityIndexes(t *testing.T) {
	mk := func(providers ...ServerOutcome) *resultsCity {
		return &resultsCity{name: providers[0].Server.City, providers: providers}
	}
	slow := mk(ServerOutcome{
		Server: srv("A", "Slow", "b"),
		Result: PingResult{Recv: 640, Median: 90 * time.Millisecond, LossPercent: 5.0},
	})
	fast := mk(ServerOutcome{
		Server: srv("B", "Fast", "a"),
		Result: PingResult{Recv: 640, Median: 10 * time.Millisecond, LossPercent: 0.1},
	})
	broken := mk(ServerOutcome{
		Server: srv("C", "Err", "c"),
		Result: PingResult{Err: errors.New("x")},
	})
	region := &resultsRegion{name: "X", cities: []*resultsCity{slow, fast, broken}}

	order := func(mode sortMode, desc bool) []string {
		idx := sortedCityIndexes(region, mode, desc)
		names := make([]string, len(idx))
		for i, j := range idx {
			names[i] = region.cities[j].name
		}
		return names
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}

	// Worst-first then best-first; the error city is unmeasurable and
	// must rank last in both directions (it is not "best").
	eq(order(sortWorstMedian, true), "A", "B", "C")
	eq(order(sortWorstMedian, false), "B", "A", "C")
	eq(order(sortWorstLoss, true), "A", "B", "C")
	eq(order(sortWorstLoss, false), "B", "A", "C")
	// Alphabetical both ways (cities are A, B, C).
	eq(order(sortName, false), "A", "B", "C")
	eq(order(sortName, true), "C", "B", "A")
	// Probe order untouched.
	eq(order(sortProbeOrder, true), "A", "B", "C")
	eq(order(sortProbeOrder, false), "A", "B", "C")
}

// TestToggleSortCycle pins the three-way header toggle: inactive →
// first direction (worst-first for metrics, A→Z for Server) → opposite
// direction → inactive.
func TestToggleSortCycle(t *testing.T) {
	test.NewApp()
	p := newResultsPane(paneConfig{})
	p.setTree(newResultsTree())

	// Metric column: desc → asc → off.
	p.toggleSort(5) // Loss
	if p.tree.mode != sortWorstLoss || !p.tree.desc {
		t.Errorf("click 1 = (%v, desc=%v), want (worstLoss, desc=true)", p.tree.mode, p.tree.desc)
	}
	p.toggleSort(5)
	if p.tree.mode != sortWorstLoss || p.tree.desc {
		t.Errorf("click 2 = (%v, desc=%v), want (worstLoss, desc=false)", p.tree.mode, p.tree.desc)
	}
	p.toggleSort(5)
	if p.tree.mode != sortProbeOrder {
		t.Errorf("click 3 = mode %v, want probe order", p.tree.mode)
	}
	// A different column starts its own cycle, then a click back on an
	// inactive column starts again (mode mismatch branch).
	p.toggleSort(0) // Server
	if p.tree.mode != sortName || p.tree.desc {
		t.Errorf("Server click 1 = (%v, desc=%v), want (name, asc)", p.tree.mode, p.tree.desc)
	}
	p.toggleSort(0)
	if p.tree.mode != sortName || !p.tree.desc {
		t.Errorf("Server click 2 = (%v, desc=%v), want (name, desc)", p.tree.mode, p.tree.desc)
	}
	p.toggleSort(0)
	if p.tree.mode != sortProbeOrder {
		t.Errorf("Server click 3 = mode %v, want probe order", p.tree.mode)
	}
}

func TestParseCellMetric(t *testing.T) {
	if v, ok := parseCellMetric(tableCell{text: "28.4"}); !ok || v != 28.4 {
		t.Errorf("parse 28.4 = (%v, %v)", v, ok)
	}
	if v, ok := parseCellMetric(tableCell{text: "1.60%"}); !ok || v != 1.60 {
		t.Errorf("parse 1.60%% = (%v, %v)", v, ok)
	}
	if v, ok := parseCellMetric(tableCell{text: "41.2ms"}); !ok || v != 41.2 {
		t.Errorf("parse 41.2ms = (%v, %v)", v, ok)
	}
	for _, bad := range []string{"—", "n/a", "error", "", "error: boom"} {
		if _, ok := parseCellMetric(tableCell{text: bad}); ok {
			t.Errorf("parse %q should fail", bad)
		}
	}
}

func TestSavedWindowSize(t *testing.T) {
	prefs := newTestPrefs(t)
	def := fyne.NewSize(960, 600)
	// No prefs written: default.
	if got := savedWindowSize(prefs, def); got != def {
		t.Errorf("empty prefs = %v, want default %v", got, def)
	}
	prefs.SetInt(prefsWindowWidthKey, 1200)
	prefs.SetInt(prefsWindowHeightKey, 800)
	if got := savedWindowSize(prefs, def); got.Width != 1200 || got.Height != 800 {
		t.Errorf("saved prefs = %v, want 1200x800", got)
	}
	// Out-of-range: default.
	prefs.SetInt(prefsWindowWidthKey, 100)
	if got := savedWindowSize(prefs, def); got != def {
		t.Errorf("out-of-range width = %v, want default", got)
	}
}

// TestLegendContentCompact guards the legend spacing: section gaps must
// be small GridWrap rows, not full-height empty labels (those made the
// spacing read as double line breaks and pushed a scrollbar into the
// dialog). Also caps the body height so the scroll — sized to fit —
// stays a safety net rather than the default experience.
func TestLegendContentCompact(t *testing.T) {
	test.NewApp()
	body := legendContent()
	vbox := body.(*fyne.Container)
	for i, child := range vbox.Objects {
		if l, ok := child.(*widget.Label); ok && l.Text == "" {
			t.Errorf("child %d is an empty full-height label — use a 6px gap GridWrap", i)
		}
	}
	h := body.MinSize().Height
	t.Logf("legend body height: %.0f", h)
	if h > 380 {
		t.Errorf("legend body is %vpx tall — spacing regressed", h)
	}
}
