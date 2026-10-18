package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWriteCSV(t *testing.T) {
	outcomes := []ServerOutcome{
		{
			Server: Server{"Chicago", "PS", "108.181.62.197"},
			Result: PingResult{
				Sent: 640, Recv: 639, LossPercent: 0.16,
				Min: 18 * time.Millisecond, Avg: 22 * time.Millisecond,
				Max: 29 * time.Millisecond, Jitter: 1500 * time.Microsecond,
			},
		},
		{
			Server: Server{"Dallas", "hivelocity", "speedtest.dal.hivelocity.net"},
			Result: PingResult{Sent: 640, Recv: 11, LossPercent: 98.28},
		},
		{
			Server: Server{"EU", "broken", "10.0.0.1"},
			Result: PingResult{Err: errors.New("network is unreachable")},
		},
	}
	regionBy := map[string]string{
		"Chicago.PS":        "NA",
		"Dallas.hivelocity": "NA",
		"EU.broken":         "EU",
	}

	var buf bytes.Buffer
	err := writeCSV(&buf, func(s Server) string { return regionBy[s.ID()] }, outcomes)
	if err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("got %d records (incl header), want 4", len(records))
	}
	header := records[0]
	wantHeader := []string{
		"region", "city", "provider", "host",
		"sent", "recv", "loss_pct",
		"avg_ms", "min_ms", "max_ms", "jitter_ms",
		"error",
	}
	if strings.Join(header, ",") != strings.Join(wantHeader, ",") {
		t.Errorf("header = %v, want %v", header, wantHeader)
	}

	row := records[1]
	if row[0] != "NA" || row[1] != "Chicago" || row[2] != "PS" {
		t.Errorf("identity cols = %v", row[:3])
	}
	if row[4] != "640" || row[5] != "639" || row[6] != "0.16" {
		t.Errorf("count cols = %v", row[4:7])
	}
	if row[7] != "22.000" || row[8] != "18.000" || row[9] != "29.000" || row[10] != "1.500" {
		t.Errorf("ms cols = %v", row[7:11])
	}
	if row[11] != "" {
		t.Errorf("error col should be empty, got %q", row[11])
	}

	// Zero-value ping (no replies) formats as 0.000, not empty.
	row2 := records[2]
	if row2[7] != "0.000" {
		t.Errorf("zero avg = %q, want 0.000", row2[7])
	}
	if row2[6] != "98.28" {
		t.Errorf("loss col = %q, want 98.28", row2[6])
	}

	// Error outcome carries the message.
	row3 := records[3]
	if !strings.Contains(row3[11], "unreachable") {
		t.Errorf("error col = %q", row3[11])
	}
	if row3[6] != "0.00" {
		t.Errorf("loss for error outcome = %q, want 0.00", row3[6])
	}
}

func TestMsPrecise(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.000"},
		{1500 * time.Microsecond, "1.500"},
		{22 * time.Millisecond, "22.000"},
		{22471 * time.Microsecond, "22.471"},
		{time.Microsecond, "0.001"},
	}
	for _, tt := range tests {
		if got := msPrecise(tt.d); got != tt.want {
			t.Errorf("msPrecise(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
