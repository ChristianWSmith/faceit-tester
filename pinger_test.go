package main

import (
	"testing"
	"time"

	"github.com/prometheus-community/pro-bing"
)

func TestBuildResultEmpty(t *testing.T) {
	res := buildResult(&probing.Statistics{})
	if res.Sent != 0 || res.Recv != 0 || res.LossPercent != 0 {
		t.Errorf("empty stats produced %+v", res)
	}
	if res.Err != nil {
		t.Errorf("unexpected error: %v", res.Err)
	}
}

func TestBuildResultFullLoss(t *testing.T) {
	res := buildResult(&probing.Statistics{PacketsSent: 640, PacketsRecv: 0})
	if res.LossPercent != 100 {
		t.Errorf("LossPercent = %v, want 100", res.LossPercent)
	}
	if res.Median != 0 || res.P1 != 0 || res.P99 != 0 {
		t.Errorf("expected zero RTTs with no replies, got %+v", res)
	}
}

func TestBuildResultNoLoss(t *testing.T) {
	// Deliberately unsorted: buildResult must sort before taking percentiles.
	rtts := []time.Duration{
		20 * time.Millisecond, 21 * time.Millisecond, 19 * time.Millisecond,
	}
	res := buildResult(&probing.Statistics{
		PacketsSent: 3,
		PacketsRecv: 3,
		PacketLoss:  0,
		Rtts:        rtts,
	})
	if res.LossPercent != 0 {
		t.Errorf("LossPercent = %v, want 0", res.LossPercent)
	}
	// Sorted: {19, 20, 21}ms → nearest-rank p50=20, p1=19, p99=21.
	if res.Median != 20*time.Millisecond || res.P1 != 19*time.Millisecond || res.P99 != 21*time.Millisecond {
		t.Errorf("percentiles not derived from Rtts: %+v", res)
	}
	// |21-20| + |19-21| = 3ms over 2 gaps = 1.5ms
	if res.Jitter != 1500*time.Microsecond {
		t.Errorf("Jitter = %v, want 1.5ms", res.Jitter)
	}
}

func TestPercentile(t *testing.T) {
	sorted := func(n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = time.Duration(i+1) * time.Millisecond
		}
		return out
	}
	// Single sample: every percentile is that sample.
	s1 := sorted(1)
	if got := percentile(s1, 50); got != time.Millisecond {
		t.Errorf("n=1 p50 = %v, want 1ms", got)
	}
	// 100 samples: nearest-rank p50 → 50th (idx 49), p1 → 1st, p99 → 99th.
	s100 := sorted(100)
	if got := percentile(s100, 50); got != 50*time.Millisecond {
		t.Errorf("n=100 p50 = %v, want 50ms", got)
	}
	if got := percentile(s100, 1); got != time.Millisecond {
		t.Errorf("n=100 p1 = %v, want 1ms", got)
	}
	if got := percentile(s100, 99); got != 99*time.Millisecond {
		t.Errorf("n=100 p99 = %v, want 99ms", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty sample = %v, want 0", got)
	}
}

func TestComputeJitterSingleAndPaired(t *testing.T) {
	if got := computeJitter(nil); got != 0 {
		t.Errorf("nil rtts: got %v", got)
	}
	if got := computeJitter([]time.Duration{time.Millisecond}); got != 0 {
		t.Errorf("single rtt: got %v", got)
	}
}

func TestProbePacing(t *testing.T) {
	tests := []struct {
		name     string
		maxPPS   int
		wantPPS  int
		wantIval time.Duration
	}{
		{"default when zero", 0, 64, time.Second / 64},
		{"default when negative", -3, 64, time.Second / 64},
		{"OVH UK policer", 9, 9, time.Second / 9},
		{"OVH fleet", 45, 45, time.Second / 45},
		{"hivelocity", 1, 1, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pps, ival := probePacing(tt.maxPPS)
			if pps != tt.wantPPS || ival != tt.wantIval {
				t.Errorf("probePacing(%d) = (%d, %v), want (%d, %v)",
					tt.maxPPS, pps, ival, tt.wantPPS, tt.wantIval)
			}
		})
	}
}

func TestIsPermissionErr(t *testing.T) {
	if isPermissionErr(nil) {
		t.Error("nil should not be a permission error")
	}
	// Errors the unprivileged socket path can produce across platforms.
	for _, msg := range []string{
		"socket: permission denied",
		"listen ip4:icmp: operation not permitted",
		"socket: The requested protocol has not been configured into the system",
	} {
		if !isPermissionErr(errString(msg)) {
			t.Errorf("expected permission error for %q", msg)
		}
	}
	if isPermissionErr(errString("network is unreachable")) {
		t.Error("network unreachable should not be treated as permission error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
