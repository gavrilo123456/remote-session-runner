package p142fixture

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestP142AddSampleMeasuresFixedWidthRecord(t *testing.T) {
	metrics := newMetrics()
	producer := time.Unix(1_800_000_000, 123_000_000)
	line := p142SampleLine(0, producer.UnixNano())
	if got, want := len(line), sampleRecordBytes-1; got != want {
		t.Fatalf("sample payload length=%d, want %d", got, want)
	}
	observed := producer.Add(237 * time.Millisecond)
	if err := metrics.addSample(2, line, observed); err != nil {
		t.Fatalf("add P142 sample: %v", err)
	}
	latencies, _, counts := metrics.snapshot()
	if counts[2] != 1 || len(latencies[2]) != 1 || latencies[2][0] != 237*time.Millisecond {
		t.Fatalf("sample metrics counts=%v latencies=%v, want one 237ms sample for worker 2", counts, latencies[2])
	}
	if counts[0] != 0 || len(latencies[0]) != 0 {
		t.Fatalf("sample was attributed to worker 0: counts=%v latencies=%v", counts, latencies[0])
	}
}

func TestP142AddSampleRejectsInvalidWidthAndSequence(t *testing.T) {
	metrics := newMetrics()
	observed := time.Unix(1_800_000_001, 0)
	if err := metrics.addSample(0, []byte("P142|"), observed); err == nil {
		t.Fatal("malformed P142 record was accepted")
	}

	line := p142SampleLine(1, observed.Add(-time.Millisecond).UnixNano())
	if err := metrics.addSample(0, line, observed); err == nil {
		t.Fatal("out-of-order P142 sample index was accepted")
	}
}

func TestP142PercentileUsesNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for index := range values {
		values[index] = time.Duration(index+1) * time.Millisecond
	}
	if got := percentile(values, 50); got != 50*time.Millisecond {
		t.Fatalf("p50=%s, want 50ms", got)
	}
	if got := percentile(values, 99); got != 99*time.Millisecond {
		t.Fatalf("p99=%s, want 99ms", got)
	}
	if got := percentile(nil, 99); got != 0 {
		t.Fatalf("empty percentile=%s, want zero", got)
	}
}

func TestP142LoadScriptHasConfiguredWorkload(t *testing.T) {
	pythonPath := "/opt/python 3/bin/python3"
	script := loadScript(pythonPath, true)
	if !bytes.Contains([]byte(script), []byte(shellQuote(pythonPath)+" -u -c ")) {
		t.Fatalf("load script does not quote Python path: %s", script)
	}
	if !bytes.Contains([]byte(script), []byte(fmt.Sprintf("range(%d)", slowSubscriberBurst/sampleRecordBytes))) {
		t.Fatalf("load script does not emit the configured startup burst")
	}
	if !bytes.Contains([]byte(script), []byte(fmt.Sprintf("range(%d)", sampleCount))) || !bytes.Contains([]byte(script), []byte("time.sleep(0.100)")) {
		t.Fatalf("load script does not emit the measured ten-minute workload")
	}

	withoutBurst := loadScript(pythonPath, false)
	if bytes.Contains([]byte(withoutBurst), []byte("burst =")) {
		t.Fatalf("non-burst command unexpectedly emits startup burst")
	}
}

func p142SampleLine(index int, timestamp int64) []byte {
	prefix := []byte(fmt.Sprintf("P142|%d|%d|", index, timestamp))
	line := append([]byte(nil), prefix...)
	line = append(line, bytes.Repeat([]byte{'x'}, sampleRecordBytes-1-len(prefix))...)
	return line
}
