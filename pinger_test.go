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
	if res.Min != 0 || res.Avg != 0 || res.Max != 0 {
		t.Errorf("expected zero RTTs with no replies, got %+v", res)
	}
}

func TestBuildResultNoLoss(t *testing.T) {
	rtts := []time.Duration{
		20 * time.Millisecond, 21 * time.Millisecond, 19 * time.Millisecond,
	}
	res := buildResult(&probing.Statistics{
		PacketsSent: 3,
		PacketsRecv: 3,
		PacketLoss:  0,
		Rtts:        rtts,
		MinRtt:      19 * time.Millisecond,
		AvgRtt:      20 * time.Millisecond,
		MaxRtt:      21 * time.Millisecond,
	})
	if res.LossPercent != 0 {
		t.Errorf("LossPercent = %v, want 0", res.LossPercent)
	}
	if res.Min != 19*time.Millisecond || res.Max != 21*time.Millisecond {
		t.Errorf("min/max not carried through: %+v", res)
	}
	// |21-20| + |19-21| = 3ms over 2 gaps = 1.5ms
	if res.Jitter != 1500*time.Microsecond {
		t.Errorf("Jitter = %v, want 1.5ms", res.Jitter)
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
