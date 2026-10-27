package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"time"
)

// Result codes encode one finished run as a single pasteable string:
//
//	base64url( 'G' | dictHash | flate( payload, dict ) )
//	payload = uvarint nServers | nServers × ( uvarint hostLen | host | metrics )
//	metrics = 9-byte block, LSB-first bit fields:
//	          loss 10b | p1 16b | med 16b | p99 16b | jitter 8b | flags 2b
//
// Codes carry ping targets plus metrics — nothing else. The host is the
// join key across players (City.Provider IDs are override-relative), and
// the decoder's own server table supplies region/city/provider at rank
// time, so the party leader always sees their own naming. The preset
// dictionary holds the embedded list's hosts (deterministic order) so
// default-list codes compress hard; the u32 fnv32a hash before the
// stream picks the decoder's matching dictionary, and a mismatch just
// inflates without one — server-list updates shrink codes, never break
// them.
//
// Quantization matches the UI's print grid so it stays invisible: loss
// in 0.01% steps (10 bits covers 0–10.22%; the value 1023 is the "dead
// or worse" sentinel and decodes as 100%), RTTs in 0.1ms steps (u16,
// clamped at 6553.5ms), jitter in 0.5ms steps (u8, clamped at 127.5ms).
// p1 is carried again — with the party view rendering the full column
// set, every metric the main table shows rides along.
const (
	codeMagic        = 'G'
	flagScrubbed     = 1 << 0
	flagErrored      = 1 << 1
	lossSentinel     = 1023  // ≥10.23% → decodes as 100%
	lossMaxQuant     = 1022  // largest honest 0.01% step in 10 bits
	msMaxDeci        = 65535 // 6553.5ms in 0.1ms units (u16)
	jitterMaxHalf    = 255   // 127.5ms in 0.5ms units (u8)
	metricBlockBytes = 9     // 68 bits padded to a whole byte per entry
	maxCodeRawBytes  = 4 << 20
)

// errNotResultCode is the single decode failure mode: a string either
// parses to a run or it doesn't. Details ride in the chain for
// debugging; the UI only ever shows the plain message.
var errNotResultCode = errors.New("not a valid result code")

// errProbeFailed stands in for a failed probe inside a decoded code —
// the original error text is not carried.
var errProbeFailed = errors.New("probe failed")

// codeDict is the flate preset dictionary: the embedded list's hosts in
// deterministic order (regionNames + slice order, first-wins). Built at
// init so every binary agrees; custom-list hosts simply don't match.
var codeDict = buildCodeDict()

var codeDictHash = fnv32a(codeDict)

func buildCodeDict() []byte {
	var b bytes.Buffer
	seen := map[string]bool{}
	for _, region := range regionNames(embeddedServers) {
		for _, s := range embeddedServers[region] {
			k := hostKey(s.Host)
			if !seen[k] {
				seen[k] = true
				b.WriteString(s.Host)
			}
		}
	}
	return b.Bytes()
}

func fnv32a(b []byte) uint32 {
	h := fnv.New32a()
	h.Write(b)
	return h.Sum32()
}

// decodedEntry is one server's results as carried in a result code: the
// ping target plus metrics. Region/city/provider are resolved by the
// decoder's own server table at rank time (resolvePartyEntries).
type decodedEntry struct {
	Host     string
	Result   PingResult
	Scrubbed bool
}

// encodeResultCode renders a run as a result code.
func encodeResultCode(outcomes []ServerOutcome) string {
	var payload bytes.Buffer
	putUvarint(&payload, uint64(len(outcomes)))
	for _, o := range outcomes {
		host := o.Server.Host
		putUvarint(&payload, uint64(len(host)))
		payload.WriteString(host)

		var loss, p1, med, p99, jitter uint64
		var flags uint64
		r := o.Result
		switch {
		case r.Err != nil:
			if o.Server.Scrubbed {
				flags = flagScrubbed
			}
			flags |= flagErrored
		default:
			p1 = uint64(quantMS(r.P1))
			med = uint64(quantMS(r.Median))
			p99 = uint64(quantMS(r.P99))
			if o.Server.Scrubbed {
				// Loss/jitter reflect the scrubber, not the path — mark
				// and zero rather than ship a misleading number.
				flags = flagScrubbed
			} else {
				loss = uint64(quantLoss(r.LossPercent))
				jitter = uint64(quantJitter(r.Jitter))
			}
		}

		var bw bitWriter
		bw.put(loss, 10)
		bw.put(p1, 16)
		bw.put(med, 16)
		bw.put(p99, 16)
		bw.put(jitter, 8)
		bw.put(flags, 2)
		payload.Write(bw.padded())
	}

	var out bytes.Buffer
	out.WriteByte(codeMagic)
	var hash [4]byte
	binary.LittleEndian.PutUint32(hash[:], codeDictHash)
	out.Write(hash[:])
	zw, _ := flate.NewWriterDict(&out, flate.BestCompression, codeDict) // dict is valid
	_, _ = zw.Write(payload.Bytes())
	_ = zw.Close()
	return base64.RawURLEncoding.EncodeToString(out.Bytes())
}

// decodeResultCode parses a result code. Every failure — bad base64,
// wrong magic, dict/framing damage, truncation, trailing bytes —
// returns errNotResultCode (wrapped): one failure mode, by design.
// Duplicate hosts inside one code keep the first occurrence.
func decodeResultCode(s string) ([]decodedEntry, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, badCode("base64")
	}
	if len(raw) < 5 {
		return nil, badCode("short")
	}
	if raw[0] != codeMagic {
		return nil, badCode("magic")
	}
	dictHash := binary.LittleEndian.Uint32(raw[1:5])
	stream := bytes.NewReader(raw[5:])
	var fr io.Reader
	if dictHash == codeDictHash {
		fr = flate.NewReaderDict(stream, codeDict)
	} else {
		fr = flate.NewReader(stream)
	}
	payload, err := io.ReadAll(io.LimitReader(fr, maxCodeRawBytes+1))
	if err != nil {
		return nil, badCode("flate")
	}
	if len(payload) > maxCodeRawBytes {
		return nil, badCode("too large")
	}
	return parseCodePayload(payload)
}

func parseCodePayload(payload []byte) ([]decodedEntry, error) {
	r := bytes.NewReader(payload)
	n, err := binary.ReadUvarint(r)
	if err != nil || n > uint64(r.Len()) {
		return nil, badCode("server count")
	}
	// Each entry is at least a 1-byte host length, 1 host byte, and the
	// 9-byte metric block.
	if n > uint64(r.Len())/11 {
		return nil, badCode("entry count")
	}
	out := make([]decodedEntry, 0, n)
	seen := make(map[string]bool, n)
	var block [metricBlockBytes]byte
	for i := uint64(0); i < n; i++ {
		hostLen, err := binary.ReadUvarint(r)
		if err != nil || hostLen > uint64(r.Len()) {
			return nil, badCode("host length")
		}
		host := make([]byte, hostLen)
		if _, err := io.ReadFull(r, host); err != nil {
			return nil, badCode("host")
		}
		if _, err := io.ReadFull(r, block[:]); err != nil {
			return nil, badCode("metrics")
		}
		var br bitReader
		br.init(block[:])
		lossQ := br.get(10)
		p1Q := br.get(16)
		medQ := br.get(16)
		p99Q := br.get(16)
		jitterQ := br.get(8)
		flags := br.get(2)

		key := hostKey(string(host))
		if seen[key] {
			continue // duplicate host inside one code: first wins
		}
		seen[key] = true

		res := PingResult{}
		if flags&flagErrored != 0 {
			res.Err = errProbeFailed
		} else {
			if lossQ == lossSentinel {
				res.LossPercent = 100
			} else {
				res.LossPercent = float64(lossQ) / 100
				res.Recv = 1 // some replies arrived, so RTT cells grade
			}
			res.P1 = time.Duration(p1Q) * 100 * time.Microsecond
			res.Median = time.Duration(medQ) * 100 * time.Microsecond
			res.P99 = time.Duration(p99Q) * 100 * time.Microsecond
			res.Jitter = time.Duration(jitterQ) * 500 * time.Microsecond
		}
		out = append(out, decodedEntry{
			Host:     string(host),
			Result:   res,
			Scrubbed: flags&flagScrubbed != 0,
		})
	}
	if r.Len() != 0 {
		return nil, badCode("trailing bytes")
	}
	return out, nil
}

func badCode(detail string) error {
	return fmt.Errorf("%w: %s", errNotResultCode, detail)
}

// quantLoss converts a loss percentage to 0.01% steps. Values at or
// above 10.23% collapse into the sentinel (they all render 100% — an
// unplayable path is an unplayable path, and 10 bits is enough
// resolution for every live number players act on).
func quantLoss(pct float64) uint16 {
	v := math.Round(pct * 100)
	if v < 0 {
		return 0
	}
	if v > lossMaxQuant {
		return lossSentinel
	}
	return uint16(v)
}

// quantMS converts a duration to 0.1ms steps (the UI's printed
// precision), clamped to the u16 range.
func quantMS(d time.Duration) uint16 {
	v := math.Round(float64(d.Microseconds()) / 100)
	if v < 0 {
		return 0
	}
	if v > msMaxDeci {
		return msMaxDeci
	}
	return uint16(v)
}

// quantJitter converts a duration to 0.5ms steps (still finer than the
// UI prints), clamped to the u8 range.
func quantJitter(d time.Duration) uint16 {
	v := math.Round(float64(d.Microseconds()) / 500)
	if v < 0 {
		return 0
	}
	if v > jitterMaxHalf {
		return jitterMaxHalf
	}
	return uint16(v)
}

func putUvarint(buf *bytes.Buffer, v uint64) {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], v)
	buf.Write(scratch[:n])
}

// bitWriter packs fields LSB-first into whole bytes; padded() zero-fills
// the final partial byte so each entry's metric block stays byte-aligned
// (uvarint host lengths follow it in the stream).
type bitWriter struct {
	b     []byte
	nbits uint
}

func (w *bitWriter) put(v uint64, bits uint) {
	for i := uint(0); i < bits; i++ {
		if w.nbits%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v&(1<<i) != 0 {
			w.b[w.nbits/8] |= 1 << (w.nbits % 8)
		}
		w.nbits++
	}
}

func (w *bitWriter) padded() []byte {
	return w.b // put() already grew b on the first bit of each byte
}

// bitReader reads LSB-first bit fields from a whole-byte block.
type bitReader struct {
	b     []byte
	nbits uint
}

func (r *bitReader) init(b []byte) {
	r.b, r.nbits = b, 0
}

func (r *bitReader) get(bits uint) uint64 {
	var v uint64
	for i := uint(0); i < bits; i++ {
		byteIdx := r.nbits / 8
		if byteIdx < uint(len(r.b)) && r.b[byteIdx]&(1<<(r.nbits%8)) != 0 {
			v |= 1 << i
		}
		r.nbits++
	}
	return v
}
