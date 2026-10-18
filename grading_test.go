package main

import (
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2/widget"
)

func TestOutcomeRowGrading(t *testing.T) {
	good := PingResult{
		Sent: 640, Recv: 640, LossPercent: 0,
		Min: 20 * time.Millisecond, Avg: 25 * time.Millisecond,
		Max: 40 * time.Millisecond, Jitter: 3 * time.Millisecond,
	}
	row := outcomeRow(ServerOutcome{Server: Server{"Chicago", "PS", "1.2.3.4"}, Result: good})
	for i, want := range []widget.Importance{
		widget.MediumImportance,  // server name
		widget.SuccessImportance, // avg 25 ≤ 100
		widget.SuccessImportance, // min 20
		widget.SuccessImportance, // max 40
		widget.SuccessImportance, // jitter 3 ≤ 10
		widget.SuccessImportance, // loss 0 ≤ 2
	} {
		if row[i].imp != want {
			t.Errorf("good row cell %d imp = %v, want %v", i, row[i].imp, want)
		}
	}

	bad := PingResult{
		Sent: 640, Recv: 400, LossPercent: 37.5,
		Min: 90 * time.Millisecond, Avg: 120 * time.Millisecond,
		Max: 200 * time.Millisecond, Jitter: 15 * time.Millisecond,
	}
	row = outcomeRow(ServerOutcome{Server: Server{"EU", "O", "5.6.7.8"}, Result: bad})
	for i, want := range []widget.Importance{
		widget.MediumImportance,
		widget.DangerImportance,  // avg 120 > 100
		widget.SuccessImportance, // min 90 ≤ 100
		widget.DangerImportance,  // max 200 > 100
		widget.DangerImportance,  // jitter 15 > 10
		widget.DangerImportance,  // loss 37.5 > 2
	} {
		if row[i].imp != want {
			t.Errorf("bad row cell %d imp = %v, want %v", i, row[i].imp, want)
		}
	}

	// Boundary: exactly at threshold is green ("at-or-below").
	at := PingResult{
		Sent: 640, Recv: 628, LossPercent: 2.0,
		Min: 100 * time.Millisecond, Avg: 100 * time.Millisecond,
		Max: 100 * time.Millisecond, Jitter: 10 * time.Millisecond,
	}
	row = outcomeRow(ServerOutcome{Server: Server{"X", "y", "9.9.9.9"}, Result: at})
	for i := 1; i <= 4; i++ {
		if row[i].imp != widget.SuccessImportance {
			t.Errorf("at-threshold cell %d imp = %v, want Success", i, row[i].imp)
		}
	}
	if row[5].imp != widget.SuccessImportance {
		t.Errorf("loss 2.0%% imp = %v, want Success (at threshold)", row[5].imp)
	}

	// Zero replies: RTT cells uncolored, loss grades (100% → red).
	none := PingResult{Sent: 640, Recv: 0, LossPercent: 100}
	row = outcomeRow(ServerOutcome{Server: Server{"X", "y", "9.9.9.9"}, Result: none})
	for i := 1; i <= 4; i++ {
		if row[i].imp != widget.MediumImportance {
			t.Errorf("no-sample cell %d imp = %v, want Medium", i, row[i].imp)
		}
	}
	if row[5].imp != widget.DangerImportance {
		t.Errorf("loss 100%% imp = %v, want Danger", row[5].imp)
	}

	// Error row: error text red.
	errRow := outcomeRow(ServerOutcome{
		Server: Server{"X", "y", "9.9.9.9"},
		Result: PingResult{Err: errors.New("boom")},
	})
	if errRow[5].imp != widget.DangerImportance {
		t.Errorf("error cell imp = %v, want Danger", errRow[5].imp)
	}
}

func TestCityWorstCase(t *testing.T) {
	outcomes := []ServerOutcome{
		{Server: Server{"Chicago", "PS", "a"}, Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0,
			Min: 18 * time.Millisecond, Avg: 22 * time.Millisecond,
			Max: 30 * time.Millisecond, Jitter: 2 * time.Millisecond,
		}},
		{Server: Server{"Chicago", "cdn77", "b"}, Result: PingResult{
			Sent: 640, Recv: 600, LossPercent: 6.25,
			Min: 40 * time.Millisecond, Avg: 55 * time.Millisecond,
			Max: 90 * time.Millisecond, Jitter: 12 * time.Millisecond,
		}},
		{Server: Server{"Chicago", "nfo", "c"}, Result: PingResult{
			Err: errors.New("network is unreachable"),
		}},
	}
	row := cityWorstCase("Chicago", outcomes)

	if row[0].text != "Chicago (worst)" {
		t.Errorf("name = %q, want %q", row[0].text, "Chicago (worst)")
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

	// All providers errored → error row, still bold.
	allErr := cityWorstCase("NY", []ServerOutcome{
		{Result: PingResult{Err: errors.New("x")}},
		{Result: PingResult{Err: errors.New("y")}},
	})
	if allErr[0].text != "NY (worst)" || !allErr[0].bold {
		t.Errorf("all-err name cell = %+v", allErr[0])
	}
	if allErr[5].imp != widget.DangerImportance || allErr[5].text != "error: x" {
		t.Errorf("all-err loss cell = %+v", allErr[5])
	}

	// Single provider: worst equals that provider's numbers.
	single := cityWorstCase("Lima", []ServerOutcome{
		{Result: PingResult{
			Sent: 640, Recv: 640, LossPercent: 0,
			Min: 90 * time.Millisecond, Avg: 93 * time.Millisecond,
			Max: 100 * time.Millisecond, Jitter: 1 * time.Millisecond,
		}},
	})
	if single[1].text != "93.0" || single[4].text != "1.0" {
		t.Errorf("single-provider worst row = %+v", single)
	}
}
