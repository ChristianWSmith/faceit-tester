package main

import (
	"context"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus-community/pro-bing"
)

const (
	// defaultProbeInterval is ~64 packets/sec, matching CS2's send rate.
	defaultProbeInterval = time.Second / 64
	// defaultProbeSize is the default ICMP payload size. CS2 UDP payloads
	// range 400-800 bytes; 600 sits mid-range for equivalent wire behavior.
	// Providers that filter large echo packets (NFO) set Server.ProbeSize.
	defaultProbeSize = 600
	// defaultProbePPS is the send rate for servers without a MaxPPS cap.
	defaultProbePPS = 64
	// resolveTimeout bounds DNS resolution per target.
	resolveTimeout = 5 * time.Second
	// replyGrace is the receive-only window after the last probe is sent
	// (probeWindow) so in-flight echo replies can still be counted.
	replyGrace = 250 * time.Millisecond
)

// probePacing derives the send rate and per-packet interval for a probe
// window. maxPPS <= 0 selects the CS2-like default; positive values stay
// under a provider's measured ICMP policer quota.
func probePacing(maxPPS int) (pps int, interval time.Duration) {
	if maxPPS <= 0 {
		return defaultProbePPS, defaultProbeInterval
	}
	return maxPPS, time.Second / time.Duration(maxPPS)
}

// PingResult holds the outcome of probing one server. RTT columns are
// nearest-rank percentiles of the reply samples: median/p1/p99 describe
// the distribution better than avg/min/max (a single delayed reply no
// longer skews the whole row).
type PingResult struct {
	Sent        int
	Recv        int
	LossPercent float64
	Median      time.Duration
	P1          time.Duration
	P99         time.Duration
	Jitter      time.Duration
	Err         error
}

// probeHost sends ICMP echo requests to host, paced per probePacing(maxPPS)
// with a payload of size bytes (<=0 selects defaultProbeSize). ctx should
// carry a deadline of probeWindow+replyGrace (the runner supplies this) so
// sends stop once Count probes are sent and the remaining time is
// receive-only for late replies. ctx cancellation aborts immediately.
// onTick, if non-nil, is called from the pinger goroutines with running
// sent/received counts; callers must marshal to the UI thread themselves.
func probeHost(ctx context.Context, host string, maxPPS, size int, onTick func(sent, recv int)) PingResult {
	privileged := runtime.GOOS == "windows"
	result, err := probeOnce(ctx, host, maxPPS, size, privileged, onTick)
	if err != nil && runtime.GOOS != "windows" && isPermissionErr(err) {
		// Unprivileged datagram ICMP unavailable (ping_group_range etc.);
		// retry with raw sockets.
		result, err = probeOnce(ctx, host, maxPPS, size, true, onTick)
	}
	if err != nil && result.Sent == 0 {
		result.Err = err
	}
	return result
}

func probeOnce(ctx context.Context, host string, maxPPS, size int, privileged bool, onTick func(sent, recv int)) (PingResult, error) {
	pps, interval := probePacing(maxPPS)
	if size <= 0 {
		size = defaultProbeSize
	}
	p, err := probing.NewPinger(host)
	if err != nil {
		return PingResult{}, err
	}
	p.SetPrivileged(privileged)
	// Count is derived so paced probes fill the same window as the default
	// rate. pro-bing stops sending once Count is reached but keeps
	// receiving until the context expires, so the runner's
	// probeWindow+replyGrace budget gives a receive-only window for late
	// replies after the last send.
	p.Count = pps * int(probeWindow/time.Second)
	p.Interval = interval
	p.Size = size
	p.ResolveTimeout = resolveTimeout
	p.RecordRtts = true

	var sent, recv atomic.Int64
	if onTick != nil {
		p.OnSend = func(*probing.Packet) {
			onTick(int(sent.Add(1)), int(recv.Load()))
		}
		p.OnRecv = func(*probing.Packet) {
			r := int(recv.Add(1))
			onTick(int(sent.Load()), r)
		}
	} else {
		p.OnSend = func(*probing.Packet) { sent.Add(1) }
		p.OnRecv = func(*probing.Packet) { recv.Add(1) }
	}

	// The runner's context carries the full probeWindow+replyGrace budget;
	// supply a default when called without a deadline (e.g. tests).
	runCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		runCtx, cancel = context.WithTimeout(ctx, probeWindow+replyGrace)
		defer cancel()
	}

	runErr := p.RunWithContext(runCtx)
	stats := p.Statistics()
	result := buildResult(stats)
	if runErr != nil && result.Sent == 0 {
		return result, runErr
	}
	return result, nil
}

func buildResult(stats *probing.Statistics) PingResult {
	res := PingResult{
		Sent: stats.PacketsSent,
		Recv: stats.PacketsRecv,
	}
	if stats.PacketsSent > 0 {
		res.LossPercent = float64(stats.PacketsSent-stats.PacketsRecv) / float64(stats.PacketsSent) * 100
	}
	if len(stats.Rtts) > 0 {
		sorted := append([]time.Duration(nil), stats.Rtts...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		res.Median = percentile(sorted, 50)
		res.P1 = percentile(sorted, 1)
		res.P99 = percentile(sorted, 99)
		res.Jitter = computeJitter(stats.Rtts)
	}
	return res
}

// percentile returns the nearest-rank percentile p of a non-empty
// ascending-sorted sample: the value at ceil(p/100*n)-1.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// computeJitter returns the mean absolute difference between consecutive RTT
// samples (RFC 3550 style).
func computeJitter(rtts []time.Duration) time.Duration {
	if len(rtts) < 2 {
		return 0
	}
	var sum time.Duration
	for i := 1; i < len(rtts); i++ {
		d := rtts[i] - rtts[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / time.Duration(len(rtts)-1)
}

func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "Access is denied") ||
		strings.Contains(msg, "not been configured into the system") ||
		strings.Contains(msg, "not configured into the system")
}
