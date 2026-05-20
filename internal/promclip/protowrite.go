package promclip

// Minimal hand-rolled protobuf encoder for the Prometheus remote-write
// schema. We only encode (not decode) the four message types we need;
// pulling in the full prometheus/prometheus module would drag dozens
// of unrelated transitive deps.
//
// The proto definitions we encode (from prompb/remote.proto and
// prompb/types.proto, simplified to what remote-write 1.0 ingests):
//
//   message WriteRequest {
//       repeated TimeSeries timeseries = 1;
//   }
//   message TimeSeries {
//       repeated Label  labels  = 1;
//       repeated Sample samples = 2;
//   }
//   message Label {
//       string name  = 1;
//       string value = 2;
//   }
//   message Sample {
//       double value     = 1;
//       int64  timestamp = 2;
//   }

import (
	"encoding/binary"
	"math"
)

// PBLabel is a label pair in a TimeSeries.
type PBLabel struct {
	Name, Value string
}

// PBSample is a (value, timestamp_ms) pair.
type PBSample struct {
	Value       float64
	TimestampMs int64
}

// PBTimeSeries is one series and its samples (already sorted by ts asc).
type PBTimeSeries struct {
	Labels  []PBLabel
	Samples []PBSample
}

// MarshalWriteRequest builds the snappy-uncompressed protobuf body of a
// remote-write `WriteRequest` carrying the supplied series.
func MarshalWriteRequest(series []PBTimeSeries) []byte {
	// Pre-compute size to allocate once.
	total := 0
	tsBufs := make([][]byte, len(series))
	for i, ts := range series {
		tsBufs[i] = marshalTimeSeries(ts)
		total += tagLen(1, 2) + varintLen(uint64(len(tsBufs[i]))) + len(tsBufs[i])
	}
	buf := make([]byte, 0, total)
	for _, b := range tsBufs {
		buf = appendTag(buf, 1, 2)
		buf = appendVarint(buf, uint64(len(b)))
		buf = append(buf, b...)
	}
	return buf
}

func marshalTimeSeries(ts PBTimeSeries) []byte {
	// labels
	labelBufs := make([][]byte, len(ts.Labels))
	size := 0
	for i, l := range ts.Labels {
		labelBufs[i] = marshalLabel(l)
		size += tagLen(1, 2) + varintLen(uint64(len(labelBufs[i]))) + len(labelBufs[i])
	}
	// samples
	sampleBufs := make([][]byte, len(ts.Samples))
	for i, s := range ts.Samples {
		sampleBufs[i] = marshalSample(s)
		size += tagLen(2, 2) + varintLen(uint64(len(sampleBufs[i]))) + len(sampleBufs[i])
	}
	out := make([]byte, 0, size)
	for _, b := range labelBufs {
		out = appendTag(out, 1, 2)
		out = appendVarint(out, uint64(len(b)))
		out = append(out, b...)
	}
	for _, b := range sampleBufs {
		out = appendTag(out, 2, 2)
		out = appendVarint(out, uint64(len(b)))
		out = append(out, b...)
	}
	return out
}

func marshalLabel(l PBLabel) []byte {
	size := tagLen(1, 2) + varintLen(uint64(len(l.Name))) + len(l.Name) +
		tagLen(2, 2) + varintLen(uint64(len(l.Value))) + len(l.Value)
	out := make([]byte, 0, size)
	out = appendTag(out, 1, 2)
	out = appendVarint(out, uint64(len(l.Name)))
	out = append(out, l.Name...)
	out = appendTag(out, 2, 2)
	out = appendVarint(out, uint64(len(l.Value)))
	out = append(out, l.Value...)
	return out
}

func marshalSample(s PBSample) []byte {
	// value (fixed64 double) + timestamp (int64 varint)
	out := make([]byte, 0, tagLen(1, 1)+8+tagLen(2, 0)+varintLen(uint64(s.TimestampMs)))
	out = appendTag(out, 1, 1)
	var fb [8]byte
	binary.LittleEndian.PutUint64(fb[:], math.Float64bits(s.Value))
	out = append(out, fb[:]...)
	out = appendTag(out, 2, 0)
	// int64 encoded as varint (NOT zigzag — proto3 int64 uses plain varint).
	// Negative int64 values fill all 10 bytes; remote-write timestamps are
	// always >= 0 in practice.
	out = appendVarint(out, uint64(s.TimestampMs))
	return out
}

func appendTag(b []byte, field, wire uint64) []byte {
	return appendVarint(b, (field<<3)|wire)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func tagLen(field, wire uint64) int {
	return varintLen((field << 3) | wire)
}
