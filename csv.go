package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"
)

// writeCSV exports test outcomes as CSV. Values use 3 decimal places for
// millisecond columns (finer than the UI's 1) to support post-hoc analysis
// such as deriving per-provider rate limits.
func writeCSV(w io.Writer, regionOf func(Server) string, outcomes []ServerOutcome) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"region", "city", "provider", "host",
		"sent", "recv", "loss_pct",
		"median_ms", "p1_ms", "p99_ms", "jitter_ms",
		"max_pps", "probe_size",
		"notes",
		"error",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, o := range outcomes {
		r := o.Result
		errStr := ""
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		lossStr := fmt.Sprintf("%.2f", r.LossPercent)
		jitterStr := msPrecise(r.Jitter)
		notes := ""
		if o.Server.Scrubbed {
			// Loss/jitter reflect the DDoS scrubber, not the path.
			lossStr = ""
			jitterStr = ""
			notes = "icmp-scrubbed"
		}
		row := []string{
			regionOf(o.Server),
			o.Server.City,
			o.Server.Provider,
			o.Server.Host,
			strconv.Itoa(r.Sent),
			strconv.Itoa(r.Recv),
			lossStr,
			msPrecise(r.Median),
			msPrecise(r.P1),
			msPrecise(r.P99),
			jitterStr,
			strconv.Itoa(o.Server.MaxPPS),
			strconv.Itoa(o.Server.ProbeSize),
			notes,
			errStr,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	if err := cw.Error(); err != nil {
		return err
	}
	return nil
}

// msPrecise formats d as milliseconds with 3 decimal places. Zero durations
// (e.g. no replies) format as "0.000".
func msPrecise(d time.Duration) string {
	if d == 0 {
		return "0.000"
	}
	return strconv.FormatFloat(float64(d.Microseconds())/1000.0, 'f', 3, 64)
}
