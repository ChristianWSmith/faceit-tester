package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestResultCodeRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		server Server
		result PingResult
		exact  bool // values on the quantization grid
	}{
		{
			name:   "grid values exact",
			server: Server{City: "San Jose", Provider: "Dedicated.com", Host: "203.0.113.7"},
			result: PingResult{
				Sent: 640, Recv: 640, LossPercent: 0.31,
				Median: 24100 * time.Microsecond, P1: 20000 * time.Microsecond,
				P99: 40200 * time.Microsecond, Jitter: 1500 * time.Microsecond,
			},
			exact: true,
		},
		{
			name:   "off-grid rounds to nearest",
			server: Server{City: "Frankfurt", Provider: "Hivelocity", Host: "metrics.example.net"},
			result: PingResult{
				Sent: 640, Recv: 630, LossPercent: 0.333,
				Median: 92149 * time.Microsecond, P1: 45100 * time.Microsecond,
				P99: 21050 * time.Microsecond, Jitter: 1800 * time.Microsecond,
			},
			exact: false,
		},
		{
			name:   "unicode host",
			server: Server{City: "Łódź", Provider: "Psychz Networks", Host: "serwery.łódź.example"},
			result: PingResult{Sent: 640, Recv: 640, Median: 31000 * time.Microsecond, P99: 55000 * time.Microsecond, P1: 25000 * time.Microsecond, Jitter: 2000 * time.Microsecond},
		},
		{
			name:   "scrubbed",
			server: Server{City: "Dallas", Provider: "Hivelocity", Host: "192.0.2.44", Scrubbed: true},
			result: PingResult{Sent: 640, Recv: 640, LossPercent: 42.5, Median: 18000 * time.Microsecond, P99: 20000 * time.Microsecond, Jitter: 9000 * time.Microsecond},
		},
		{
			name:   "errored",
			server: Server{City: "São Paulo", Provider: "Latitude.sh", Host: "192.0.2.99"},
			result: PingResult{Err: errors.New("request timeout")},
		},
		{
			name:   "scrubbed and errored",
			server: Server{City: "Sydney", Provider: "OVH", Host: "192.0.2.53", Scrubbed: true},
			result: PingResult{Err: errors.New("no route")},
		},
		{
			name:   "total loss",
			server: Server{City: "Singapore", Provider: "NForce", Host: "192.0.2.60"},
			result: PingResult{Sent: 640, Recv: 0, LossPercent: 100},
		},
		{
			name:   "partial loss",
			server: Server{City: "Denver", Provider: "Handy Networks", Host: "173.248.162.1"},
			result: PingResult{Sent: 640, Recv: 587, LossPercent: 8.13},
		},
		{
			name:   "loss over the sentinel renders dead",
			server: Server{City: "Lima", Provider: "OVH", Host: "192.0.2.70"},
			result: PingResult{Sent: 640, Recv: 320, LossPercent: 50},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []ServerOutcome{{Server: tc.server, Result: tc.result}}
			code := encodeResultCode(in)

			out, err := decodeResultCode(code)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(out) != 1 {
				t.Fatalf("decoded %d entries, want 1", len(out))
			}
			got := out[0]
			if got.Host != tc.server.Host {
				t.Errorf("host = %q, want %q", got.Host, tc.server.Host)
			}
			if got.Scrubbed != tc.server.Scrubbed {
				t.Errorf("scrubbed = %v, want %v", got.Scrubbed, tc.server.Scrubbed)
			}

			r, res := got.Result, tc.result
			switch {
			case res.Err != nil:
				if !errors.Is(r.Err, errProbeFailed) {
					t.Errorf("err = %v, want errProbeFailed", r.Err)
				}
			default:
				if r.Err != nil {
					t.Fatalf("unexpected error: %v", r.Err)
				}
				if tc.name == "loss over the sentinel renders dead" {
					// ≥10.23% collapses into the sentinel and reads 100%.
					if r.LossPercent != 100 || r.Recv != 0 {
						t.Errorf("sentinel loss/recv = %v/%v, want 100/0", r.LossPercent, r.Recv)
					}
					return
				}
				// Recv is carried as presence, not count (100% loss = no
				// replies, so RTT cells stay ungraded).
				wantRecv := res.Recv > 0 && res.LossPercent < 100
				if (r.Recv > 0) != wantRecv {
					t.Errorf("recv presence = %v, want %v", r.Recv > 0, wantRecv)
				}
				if tc.server.Scrubbed {
					if r.LossPercent != 0 || r.Jitter != 0 {
						t.Errorf("scrubbed loss/jitter = %v/%v, want 0/0 (not measurable)", r.LossPercent, r.Jitter)
					}
					if r.Median != res.Median {
						t.Errorf("scrubbed median = %v, want %v (RTT stays valid)", r.Median, res.Median)
					}
					return
				}
				if tc.exact {
					if r.LossPercent != tc.result.LossPercent {
						t.Errorf("loss = %v, want %v", r.LossPercent, tc.result.LossPercent)
					}
					if r.P1 != res.P1 || r.Median != res.Median || r.P99 != res.P99 || r.Jitter != res.Jitter {
						t.Errorf("rtts = %v/%v/%v/%v, want %v/%v/%v/%v",
							r.P1, r.Median, r.P99, r.Jitter, res.P1, res.Median, res.P99, res.Jitter)
					}
				} else {
					if d := r.LossPercent - tc.result.LossPercent; d > 0.0051 || d < -0.0051 {
						t.Errorf("loss %v not within half a step of %v", r.LossPercent, tc.result.LossPercent)
					}
					assertHalfStep(t, "p1", r.P1, res.P1, 50*time.Microsecond)
					assertHalfStep(t, "median", r.Median, res.Median, 50*time.Microsecond)
					assertHalfStep(t, "p99", r.P99, res.P99, 50*time.Microsecond)
					assertHalfStep(t, "jitter", r.Jitter, res.Jitter, 250*time.Microsecond)
				}
			}
		})
	}
}

func assertHalfStep(t *testing.T, name string, got, want time.Duration, half time.Duration) {
	t.Helper()
	if d := got - want; d > half || d < -half {
		t.Errorf("%s %v not within %v of %v", name, got, half, want)
	}
}

// TestResultCodeCharset pins the paste-safe alphabet: URL-safe base64
// with no padding (Discord/clipboard friendly, no + / = that get
// mangled in transit).
func TestResultCodeCharset(t *testing.T) {
	code := encodeResultCode([]ServerOutcome{
		{Server: Server{City: "X", Provider: "Y", Host: "192.0.2.1"},
			Result: PingResult{Sent: 640, Recv: 640, Median: time.Millisecond}},
	})
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(code) {
		t.Errorf("code %q is not URL-safe base64", code)
	}
}

// TestResultCodeRejects drives the single failure mode: every kind of
// garbage maps to errNotResultCode. There is no version negotiation —
// a code either parses or it doesn't.
func TestResultCodeRejects(t *testing.T) {
	good := encodeResultCode([]ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "OVH", Host: "192.0.2.1"},
			Result: PingResult{Sent: 640, Recv: 640, Median: 20 * time.Millisecond, P99: 30 * time.Millisecond}},
	})
	raw := mustDecode64(t, good) // magic + hash + flate stream

	cases := map[string]string{
		"empty":           "",
		"whitespace":      "   \n ",
		"not base64":      "!!! not a code !!!",
		"base64 garbage":  base64.RawURLEncoding.EncodeToString([]byte("hello world")),
		"truncated frame": good[:len(good)-8],
		"empty raw":       base64.RawURLEncoding.EncodeToString([]byte{codeMagic, 0, 0, 0, 0}),
		"flate garbage":   base64.RawURLEncoding.EncodeToString(append([]byte{codeMagic, 0, 0, 0, 0}, []byte("not flate")...)),
	}
	// Structured: correct frame but a payload that breaks a parser
	// invariant (rebuilt through the same flate framing).
	cases["server count too large"] = rawToCode(t, append([]byte{0xff, 0x01, 0x00}, make([]byte, 32)...))
	cases["entry count too large"] = rawToCode(t, mustUvarint(t, 1) /* n=1 */)
	cases["trailing bytes"] = rawToCode(t, append(payloadOf(t, good), 0x00))
	cases["wrong magic"] = base64.RawURLEncoding.EncodeToString(
		append([]byte{'X', raw[1], raw[2], raw[3], raw[4]}, raw[5:]...))
	// A dict-compressed stream with a wrong hash must still decode via
	// the plain path — so craft a PLAIN-flate payload with a non-matching
	// hash instead: that's valid, not a rejection. The mismatch path
	// itself is pinned by TestResultCodeDictHashMismatchFallsBack.

	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeResultCode(code)
			if !errors.Is(err, errNotResultCode) {
				t.Errorf("err = %v, want errNotResultCode", err)
			}
		})
	}
}

// TestResultCodeDictHashMismatchFallsBack: a payload compressed without
// the dictionary (or with a different one, e.g. after a server-list
// update changed codeDict) still decodes — the hash only picks the
// faster path, it never gates correctness.
func TestResultCodeDictHashMismatchFallsBack(t *testing.T) {
	payload := payloadOf(t, encodeResultCode([]ServerOutcome{
		{Server: Server{City: "Dallas", Provider: "OVH", Host: "192.0.2.9"},
			Result: PingResult{Sent: 640, Recv: 640, Median: 42100 * time.Microsecond}},
	}))
	var plain bytes.Buffer
	zw, _ := flate.NewWriter(&plain, flate.BestCompression)
	zw.Write(payload)
	zw.Close()
	var framed bytes.Buffer
	framed.WriteByte(codeMagic)
	binary.Write(&framed, binary.LittleEndian, uint32(0xdeadbeef)) // not our hash
	framed.Write(plain.Bytes())

	out, err := decodeResultCode(base64.RawURLEncoding.EncodeToString(framed.Bytes()))
	if err != nil {
		t.Fatalf("decode without dict: %v", err)
	}
	if len(out) != 1 || out[0].Host != "192.0.2.9" || out[0].Result.Median != 42100*time.Microsecond {
		t.Errorf("decoded %+v, want the Dallas entry", out)
	}
}

// TestResultCodeDuplicateHostFirstWins: the host is the ground-truth
// identity, so one code carrying the same target twice (possible via
// overrides) keeps the first entry.
func TestResultCodeDuplicateHostFirstWins(t *testing.T) {
	in := []ServerOutcome{
		{Server: Server{City: "Chicago", Provider: "NFOservers", Host: "74.91.113.173"},
			Result: PingResult{Sent: 640, Recv: 640, Median: 25000 * time.Microsecond, P99: 40000 * time.Microsecond}},
		{Server: Server{City: "Chicago", Provider: "Other", Host: "74.91.113.173"},
			Result: PingResult{Sent: 640, Recv: 640, Median: 90000 * time.Microsecond, P99: 99000 * time.Microsecond}},
	}
	out, err := decodeResultCode(encodeResultCode(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Result.Median != 25000*time.Microsecond {
		t.Errorf("entries = %+v, want one with the first median", out)
	}
	// Host comparison is case-insensitive (DNS): same host, different case.
	in[1].Server.Host = strings.ToUpper(in[0].Server.Host)
	out, err = decodeResultCode(encodeResultCode(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("case-variant entries = %d, want 1", len(out))
	}
}

func TestResultCodeEmptyRun(t *testing.T) {
	out, err := decodeResultCode(encodeResultCode(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("entries = %d, want 0", len(out))
	}
}

// TestResultCodeWithinDiscordBudget pins the shareable size: a full run
// over the embedded list must fit in one Discord message (2000 chars).
// Payload size is value-independent (fixed-width metrics), so the number
// is deterministic per server list; if this fails, the list grew or a
// squeeze regressed.
func TestResultCodeWithinDiscordBudget(t *testing.T) {
	var outcomes []ServerOutcome
	for _, region := range regionNames(embeddedServers) {
		for i, s := range embeddedServers[region] {
			loss := 0.0
			if i%7 == 3 {
				loss = float64(i%5) * 0.31
			}
			med := time.Duration(15+i*3%130) * time.Millisecond
			outcomes = append(outcomes, ServerOutcome{
				Server: s,
				Result: PingResult{
					Sent: 640, Recv: 640, LossPercent: loss,
					Median: med, P1: med * 4 / 5, P99: med * 2,
					Jitter: time.Duration(2+i%8) * time.Millisecond,
				},
			})
		}
	}
	code := encodeResultCode(outcomes)
	t.Logf("full-run code: %d chars for %d servers", len(code), len(outcomes))
	if len(code) >= 2000 {
		t.Errorf("code is %d chars, must stay under the 2000-char Discord limit", len(code))
	}
	out, err := decodeResultCode(code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(outcomes) {
		t.Errorf("decoded %d servers, want %d", len(out), len(outcomes))
	}
}

// --- helpers ---

func mustDecode64(t *testing.T, code string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// payloadOf extracts the flate-decompressed payload of a valid code.
func payloadOf(t *testing.T, code string) []byte {
	t.Helper()
	raw := mustDecode64(t, code)
	zr := flate.NewReader(bytes.NewReader(raw[5:]))
	payload, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// rawToCode frames a raw payload with a non-dict hash + plain flate.
func rawToCode(t *testing.T, payload []byte) string {
	t.Helper()
	var z bytes.Buffer
	zw, _ := flate.NewWriter(&z, flate.BestCompression)
	zw.Write(payload)
	zw.Close()
	var out bytes.Buffer
	out.WriteByte(codeMagic)
	binary.Write(&out, binary.LittleEndian, uint32(0)) // wrong hash → plain path
	out.Write(z.Bytes())
	return base64.RawURLEncoding.EncodeToString(out.Bytes())
}

func mustUvarint(t *testing.T, v uint64) []byte {
	t.Helper()
	var b bytes.Buffer
	putUvarint(&b, v)
	return b.Bytes()
}
