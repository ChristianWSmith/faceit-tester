package main

import (
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2/widget"
)

func TestWorstCase(t *testing.T) {
	outcomes := []ServerOutcome{
		{Server: srv("Chicago", "Psychz Networks", "a"), Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0,
			Median: 22 * time.Millisecond, P1: 18 * time.Millisecond,
			P99: 30 * time.Millisecond, Jitter: 2 * time.Millisecond,
		}},
		{Server: srv("Chicago", "Datacamp", "b"), Result: PingResult{
			Sent: 640, Recv: 600, LossPercent: 6.25,
			Median: 55 * time.Millisecond, P1: 40 * time.Millisecond,
			P99: 90 * time.Millisecond, Jitter: 12 * time.Millisecond,
		}},
		{Server: srv("Chicago", "NFOservers", "c"), Result: PingResult{
			Err: errors.New("network is unreachable"),
		}},
	}
	row := worstCase("Chicago (2)", outcomes)

	if row[0].text != "Chicago (2)" {
		t.Errorf("name = %q, want %q", row[0].text, "Chicago (2)")
	}
	if !row[0].bold {
		t.Error("worst-case name cell should be bold")
	}
	// Column-by-column max, error row skipped for numerics.
	wantTexts := []string{"55.0", "40.0", "90.0", "12.0", "6.25%"}
	for i, want := range wantTexts {
		if row[i+1].text != want {
			t.Errorf("worst cell %d text = %q, want %q", i+1, row[i+1].text, want)
		}
	}
	// Graded normally: 55ms ok, 12ms jitter over, 6.25% loss over.
	wantImps := []widget.Importance{
		widget.SuccessImportance,
		widget.SuccessImportance,
		widget.SuccessImportance,
		widget.DangerImportance, // jitter 12 > 10
		widget.DangerImportance, // loss 6.25 > 2
	}
	for i, want := range wantImps {
		if row[i+1].imp != want {
			t.Errorf("worst cell %d imp = %v, want %v", i+1, row[i+1].imp, want)
		}
	}

	// All outcomes errored → error row, still bold.
	allErr := worstCase("NY", []ServerOutcome{
		{Result: PingResult{Err: errors.New("x")}},
		{Result: PingResult{Err: errors.New("y")}},
	})
	if allErr[0].text != "NY" || !allErr[0].bold {
		t.Errorf("all-err name cell = %+v", allErr[0])
	}
	if allErr[5].imp != widget.DangerImportance || allErr[5].text != "error: x" {
		t.Errorf("all-err loss cell = %+v", allErr[5])
	}

	// Single outcome: worst equals that provider's numbers.
	single := worstCase("Lima", []ServerOutcome{
		{Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0,
			Median: 93 * time.Millisecond, P1: 90 * time.Millisecond,
			P99: 100 * time.Millisecond, Jitter: 1 * time.Millisecond,
		}},
	})
	if single[1].text != "93.0" || single[4].text != "1.0" {
		t.Errorf("single-provider worst row = %+v", single)
	}

	// Scrubbed outcomes feed the RTT columns but never jitter/loss.
	mixed := worstCase("Dallas", []ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "Psychz Networks", Host: "a"}, Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0.2,
			Median: 50 * time.Millisecond, P1: 45 * time.Millisecond,
			P99: 60 * time.Millisecond, Jitter: 3 * time.Millisecond,
		}},
		{Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "b", Scrubbed: true}, Result: PingResult{
			Sent: 640, Recv: 10, LossPercent: 98.4,
			Median: 49 * time.Millisecond, P1: 46 * time.Millisecond,
			P99: 52 * time.Millisecond, Jitter: 25 * time.Millisecond,
		}},
	})
	if mixed[1].text != "50.0" {
		t.Errorf("mixed worst avg = %q, want 50.0", mixed[1].text)
	}
	if mixed[4].text != "3.0" || mixed[4].imp != widget.SuccessImportance {
		t.Errorf("mixed worst jitter = %+v, want 3.0 Success (scrubber excluded)", mixed[4])
	}
	if mixed[5].text != "0.20%" {
		t.Errorf("mixed worst loss = %q, want 0.20%% (scrubber excluded)", mixed[5].text)
	}

	// Only scrubbed outcomes: jitter/loss show n/a.
	allScrub := worstCase("Ghost Town", []ServerOutcome{
		{Server: Server{City: "Ghost Town", Provider: "Hivelocity", Host: "c", Scrubbed: true}, Result: PingResult{
			Sent: 640, Recv: 10, LossPercent: 98.4,
			Median: 49 * time.Millisecond, P1: 46 * time.Millisecond,
			P99: 52 * time.Millisecond, Jitter: 25 * time.Millisecond,
		}},
	})
	if allScrub[1].text != "49.0" {
		t.Errorf("all-scrubbed worst avg = %q, want 49.0", allScrub[1].text)
	}
	for i := 4; i <= 5; i++ {
		if allScrub[i].text != "n/a" || allScrub[i].imp != widget.MediumImportance {
			t.Errorf("all-scrubbed cell %d = %+v, want uncolored n/a", i, allScrub[i])
		}
	}
}

// TestFlatten covers the results-tree rendering: hierarchy, labels,
// worst-case aggregation at city and region level, and expansion.
func TestFlatten(t *testing.T) {
	tr := newResultsTree()
	tr.addOutcome("NA", ServerOutcome{
		Server: srv("Chicago", "Psychz Networks", "a"),
		Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0,
			Median: 22 * time.Millisecond, P1: 18 * time.Millisecond,
			P99: 30 * time.Millisecond, Jitter: 2 * time.Millisecond,
		},
	})
	tr.addOutcome("NA", ServerOutcome{
		Server: srv("Chicago", "Datacamp", "b"),
		Result: PingResult{
			Sent: 640, Recv: 600, LossPercent: 6.25,
			Median: 55 * time.Millisecond, P1: 40 * time.Millisecond,
			P99: 90 * time.Millisecond, Jitter: 12 * time.Millisecond,
		},
	})
	tr.addOutcome("EU", ServerOutcome{
		Server: srv("UK", "OVH", "c"),
		Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 1.0,
			Median: 90 * time.Millisecond, P1: 80 * time.Millisecond,
			P99: 95 * time.Millisecond, Jitter: 5 * time.Millisecond,
		},
	})

	rows := tr.flatten()
	// Regions start expanded, cities collapsed: 2 region rows + 2 city
	// rows, leaves hidden until their city is tapped.
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	wantNames := []string{
		"▾ NA (2)",
		"    ▸ Chicago (2)",
		"▾ EU (1)",
		"    ▸ UK (1)",
	}
	for i, want := range wantNames {
		if rows[i][0].text != want {
			t.Errorf("row %d name = %q, want %q", i, rows[i][0].text, want)
		}
	}
	// Region rows aggregate the worst of their cities (= worst of all
	// their providers, hidden leaves included): NA worst is Datacamp.
	naRegion := rows[0]
	if !naRegion[0].bold {
		t.Error("region row name should be bold")
	}
	if naRegion[1].text != "55.0" || naRegion[4].text != "12.0" || naRegion[5].text != "6.25%" {
		t.Errorf("NA region aggregate = %v, want Datacamp worst", naRegion[1:])
	}
	if naRegion[1].imp != widget.SuccessImportance ||
		naRegion[4].imp != widget.DangerImportance ||
		naRegion[5].imp != widget.DangerImportance {
		t.Errorf("NA region grading = %v, want worst-case grading", naRegion[1:])
	}
	// EU single-city region matches the city row.
	euRegion := rows[2]
	if euRegion[1].text != "90.0" || euRegion[5].text != "1.00%" {
		t.Errorf("EU region aggregate = %v, want UK numbers", euRegion[1:])
	}

	// Collapsing NA hides its city row.
	if !tr.toggle(0) {
		t.Fatal("toggle(0) should collapse the NA region")
	}
	rows = tr.flatten()
	if len(rows) != 3 {
		t.Fatalf("after NA collapse: got %d rows, want 3 (NA + expanded EU…)", len(rows))
	}
	if rows[0][0].text != "▸ NA (2)" {
		t.Errorf("collapsed region label = %q, want ▸ NA (2)", rows[0][0].text)
	}
	tr.toggle(0) // expand NA again

	// Expanding a city reveals its leaves; collapsing hides them again.
	if !tr.toggle(1) {
		t.Fatal("toggle(1) should expand the Chicago city")
	}
	rows = tr.flatten()
	if len(rows) != 6 {
		t.Fatalf("after Chicago expand: got %d rows, want 6", len(rows))
	}
	if rows[1][0].text != "    ▾ Chicago (2)" {
		t.Errorf("expanded city label = %q, want ▾ Chicago (2)", rows[1][0].text)
	}
	if rows[2][0].text != "        Chicago.Psychz Networks" {
		t.Errorf("leaf label = %q, want indented provider name", rows[2][0].text)
	}
	if rows[2][0].bold {
		t.Error("leaf row should not be bold")
	}
	tr.toggle(1) // collapse Chicago again
	rows = tr.flatten()
	if len(rows) != 4 {
		t.Fatalf("after Chicago collapse: got %d rows, want 4", len(rows))
	}
	for _, r := range rows {
		if r[0].text == "        Chicago.Datacamp" || r[0].text == "        Chicago.Psychz Networks" {
			t.Errorf("leaf %q visible while city collapsed", r[0].text)
		}
	}

	// Tapping a leaf row reports no toggle. Expand Chicago first so a
	// leaf is visible (flat row 3 = Datacamp).
	tr.toggle(1)
	if tr.toggle(3) {
		t.Error("toggle on a leaf row should return false")
	}
}

// TestFlattenScrubbedRegion verifies a region whose only measurable child
// is scrubbed shows n/a jitter/loss, while a mixed region grades from the
// measurable city only.
func TestFlattenScrubbedRegion(t *testing.T) {
	tr := newResultsTree()
	tr.addOutcome("NA", ServerOutcome{
		Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "h", Scrubbed: true},
		Result: PingResult{
			Sent: 640, Recv: 10, LossPercent: 98.4,
			Median: 49 * time.Millisecond, P1: 45 * time.Millisecond,
			P99: 55 * time.Millisecond, Jitter: 20 * time.Millisecond,
		},
	})
	rows := tr.flatten()
	// Region + collapsed city; the scrubbed leaf stays hidden by default.
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (region + collapsed city)", len(rows))
	}
	for _, row := range rows {
		for i := 4; i <= 5; i++ {
			if row[i].text != "n/a" || row[i].imp != widget.MediumImportance {
				t.Errorf("scrubbed-only aggregate cell %d = %+v, want n/a", i, row[i])
			}
		}
	}
}
