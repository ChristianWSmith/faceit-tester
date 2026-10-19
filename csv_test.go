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
			Server: srv("Chicago", "Psychz Networks", "108.181.62.197"),
			Result: PingResult{
				Sent: 640, Recv: 639, LossPercent: 0.16,
				Median: 22 * time.Millisecond, P1: 18 * time.Millisecond,
				P99: 29 * time.Millisecond, Jitter: 1500 * time.Microsecond,
			},
		},
		{
			Server: Server{City: "Dallas", Provider: "Hivelocity", Host: "speedtest.dal.hivelocity.net", Scrubbed: true},
			Result: PingResult{Sent: 640, Recv: 10, LossPercent: 98.4,
				Median: 49 * time.Millisecond, P1: 45 * time.Millisecond,
				P99: 55 * time.Millisecond, Jitter: 20 * time.Millisecond},
		},
		{
			Server: Server{City: "Chicago", Provider: "NFOservers", Host: "74.91.113.173", ProbeSize: 56},
			Result: PingResult{Sent: 640, Recv: 640},
		},
		{
			Server: srv("EU", "broken", "10.0.0.1"),
			Result: PingResult{Err: errors.New("network is unreachable")},
		},
	}
	regionBy := map[string]string{
		"Chicago.Psychz Networks": "NA",
		"Dallas.Hivelocity":       "NA",
		"Chicago.NFOservers":      "NA",
		"EU.broken":               "EU",
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
	if len(records) != 5 {
		t.Fatalf("got %d records (incl header), want 5", len(records))
	}
	header := records[0]
	wantHeader := []string{
		"region", "city", "provider", "host",
		"sent", "recv", "loss_pct",
		"median_ms", "p1_ms", "p99_ms", "jitter_ms",
		"max_pps", "probe_size",
		"notes",
		"error",
	}
	if strings.Join(header, ",") != strings.Join(wantHeader, ",") {
		t.Errorf("header = %v, want %v", header, wantHeader)
	}

	row := records[1]
	if row[0] != "NA" || row[1] != "Chicago" || row[2] != "Psychz Networks" {
		t.Errorf("identity cols = %v", row[:3])
	}
	if row[4] != "640" || row[5] != "639" || row[6] != "0.16" {
		t.Errorf("count cols = %v", row[4:7])
	}
	if row[7] != "22.000" || row[8] != "18.000" || row[9] != "29.000" || row[10] != "1.500" {
		t.Errorf("ms cols = %v", row[7:11])
	}
	if row[11] != "0" || row[12] != "0" {
		t.Errorf("override cols = %v, want 0 0", row[11:13])
	}
	if row[13] != "" {
		t.Errorf("notes col should be empty, got %q", row[13])
	}
	if row[14] != "" {
		t.Errorf("error col should be empty, got %q", row[14])
	}

	// Scrubbed endpoint: loss/jitter blank, note set, RTT kept.
	row2 := records[2]
	if row2[6] != "" || row2[10] != "" {
		t.Errorf("scrubbed loss/jitter = (%q, %q), want blank", row2[6], row2[10])
	}
	if row2[7] != "49.000" {
		t.Errorf("scrubbed avg = %q, want 49.000", row2[7])
	}
	if row2[11] != "0" || row2[12] != "0" {
		t.Errorf("scrubbed override cols = %v, want 0 0", row2[11:13])
	}
	if row2[13] != "icmp-scrubbed" {
		t.Errorf("scrubbed notes = %q, want icmp-scrubbed", row2[13])
	}

	// Payload-size override is exported too.
	row3 := records[3]
	if row3[11] != "0" || row3[12] != "56" {
		t.Errorf("nfo override cols = %v, want max_pps=0 probe_size=56", row3[11:13])
	}

	// Error outcome carries the message.
	row4 := records[4]
	if !strings.Contains(row4[14], "unreachable") {
		t.Errorf("error col = %q", row4[14])
	}
	if row4[6] != "0.00" {
		t.Errorf("loss for error outcome = %q, want 0.00", row4[6])
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
