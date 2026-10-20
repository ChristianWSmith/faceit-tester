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
		Median: 25 * time.Millisecond, P1: 20 * time.Millisecond,
		P99: 40 * time.Millisecond, Jitter: 3 * time.Millisecond,
	}
	row := outcomeRow(ServerOutcome{Server: srv("Chicago", "Psychz Networks", "1.2.3.4"), Result: good})
	for i, want := range []widget.Importance{
		widget.MediumImportance,  // server name
		widget.SuccessImportance, // median 25 ≤ 100
		widget.SuccessImportance, // p1 20
		widget.SuccessImportance, // p99 40
		widget.SuccessImportance, // jitter 3 ≤ 10
		widget.SuccessImportance, // loss 0 ≤ 2
	} {
		if row[i].imp != want {
			t.Errorf("good row cell %d imp = %v, want %v", i, row[i].imp, want)
		}
	}

	bad := PingResult{
		Sent: 640, Recv: 400, LossPercent: 37.5,
		Median: 120 * time.Millisecond, P1: 90 * time.Millisecond,
		P99: 200 * time.Millisecond, Jitter: 15 * time.Millisecond,
	}
	row = outcomeRow(ServerOutcome{Server: srv("EU", "OVH", "5.6.7.8"), Result: bad})
	for i, want := range []widget.Importance{
		widget.MediumImportance,
		widget.DangerImportance,  // median 120 > 100
		widget.WarningImportance, // p1 90 in [70, 100]
		widget.DangerImportance,  // p99 200 > 100
		widget.DangerImportance,  // jitter 15 > 10
		widget.DangerImportance,  // loss 37.5 > 2
	} {
		if row[i].imp != want {
			t.Errorf("bad row cell %d imp = %v, want %v", i, row[i].imp, want)
		}
	}

	// Boundary: exactly at red threshold is yellow ("strictly above" = red);
	// exactly at warn threshold is yellow too ("at-or-above warn").
	at := PingResult{
		Sent: 640, Recv: 628, LossPercent: 2.0,
		Median: 100 * time.Millisecond, P1: 100 * time.Millisecond,
		P99: 100 * time.Millisecond, Jitter: 10 * time.Millisecond,
	}
	row = outcomeRow(ServerOutcome{Server: srv("X", "y", "9.9.9.9"), Result: at})
	for i := 1; i <= 4; i++ {
		if row[i].imp != widget.WarningImportance {
			t.Errorf("at-threshold cell %d imp = %v, want Warning", i, row[i].imp)
		}
	}
	if row[5].imp != widget.WarningImportance {
		t.Errorf("loss 2.0%% imp = %v, want Warning (at threshold)", row[5].imp)
	}

	// Just under the warn thresholds is green; just under red is yellow.
	warnEdge := PingResult{
		Sent: 640, Recv: 634, LossPercent: 1.0,
		Median: 70 * time.Millisecond, P1: 69 * time.Millisecond,
		P99: 99 * time.Millisecond, Jitter: 5 * time.Millisecond,
	}
	row = outcomeRow(ServerOutcome{Server: srv("X", "y", "9.9.9.9"), Result: warnEdge})
	for i, want := range []widget.Importance{
		widget.MediumImportance,
		widget.WarningImportance, // median 70 ≥ 70
		widget.SuccessImportance, // p1 69 < 70
		widget.WarningImportance, // p99 99 in [70, 100]
		widget.WarningImportance, // jitter 5 ≥ 5
		widget.WarningImportance, // loss 1.0 ≥ 1
	} {
		if row[i].imp != want {
			t.Errorf("warn-edge cell %d imp = %v, want %v", i, row[i].imp, want)
		}
	}

	// Zero replies: RTT cells uncolored, loss grades (100% → red).
	none := PingResult{Sent: 640, Recv: 0, LossPercent: 100}
	row = outcomeRow(ServerOutcome{Server: srv("X", "y", "9.9.9.9"), Result: none})
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
		Server: srv("X", "y", "9.9.9.9"),
		Result: PingResult{Err: errors.New("boom")},
	})
	if errRow[5].imp != widget.DangerImportance {
		t.Errorf("error cell imp = %v, want Danger", errRow[5].imp)
	}

	// Scrubbed endpoint: loss/jitter uncolored "n/a", RTT still graded.
	scrubbedOut := outcomeRow(ServerOutcome{
		Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "h", Scrubbed: true},
		Result: PingResult{
			Sent: 640, Recv: 10, LossPercent: 98.4,
			P1: 45 * time.Millisecond, Median: 49 * time.Millisecond,
			P99: 55 * time.Millisecond, Jitter: 20 * time.Millisecond,
		},
	})
	if scrubbedOut[1].imp != widget.SuccessImportance {
		t.Errorf("scrubbed avg cell imp = %v, want Success", scrubbedOut[1].imp)
	}
	for i := 4; i <= 5; i++ {
		if scrubbedOut[i].text != "n/a" || scrubbedOut[i].imp != widget.MediumImportance {
			t.Errorf("scrubbed cell %d = %+v, want uncolored n/a", i, scrubbedOut[i])
		}
	}
}
