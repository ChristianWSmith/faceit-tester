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
		"avg_ms", "min_ms", "max_ms", "jitter_ms",
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
		row := []string{
			regionOf(o.Server),
			o.Server.City,
			o.Server.Provider,
			o.Server.Host,
			strconv.Itoa(r.Sent),
			strconv.Itoa(r.Recv),
			fmt.Sprintf("%.2f", r.LossPercent),
			msPrecise(r.Avg),
			msPrecise(r.Min),
			msPrecise(r.Max),
			msPrecise(r.Jitter),
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
