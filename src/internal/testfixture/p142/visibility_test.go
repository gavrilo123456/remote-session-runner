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
	appendStarted := producer.Add(25 * time.Millisecond)
	observed := producer.Add(237 * time.Millisecond)
	if err := metrics.addSample(2, line, observed, appendStarted); err != nil {
		t.Fatalf("add P142 sample: %v", err)
	}
	latencies, _, counts := metrics.snapshot()
	if counts[2] != 1 || len(latencies[2]) != 1 || latencies[2][0] != 237*time.Millisecond {
		t.Fatalf("sample metrics counts=%v latencies=%v, want one 237ms sample for worker 2", counts, latencies[2])
	}
	if counts[0] != 0 || len(latencies[0]) != 0 {
		t.Fatalf("sample was attributed to worker 0: counts=%v latencies=%v", counts, latencies[0])
	}
	preStore, storeToObserver := metrics.stageLatencySnapshot()
	if len(preStore[2]) != 1 || preStore[2][0] != 25*time.Millisecond || len(storeToObserver[2]) != 1 || storeToObserver[2][0] != 212*time.Millisecond {
		t.Fatalf("latency stages before_store=%v store_to_observer=%v, want 25ms and 212ms", preStore[2], storeToObserver[2])
	}
}

func TestP142AddSampleRejectsInvalidWidthAndSequence(t *testing.T) {
	metrics := newMetrics()
	observed := time.Unix(1_800_000_001, 0)
	if err := metrics.addSample(0, []byte("P142|"), observed, observed); err == nil {
		t.Fatal("malformed P142 record was accepted")
	}

	line := p142SampleLine(1, observed.Add(-time.Millisecond).UnixNano())
	if err := metrics.addSample(0, line, observed, observed); err == nil {
		t.Fatal("out-of-order P142 sample index was accepted")
	}
}

func TestP142AddSampleClampsAndReportsSmallClockSkew(t *testing.T) {
	metrics := newMetrics()
	producer := time.Unix(1_800_000_000, 0)
	line := p142SampleLine(0, producer.UnixNano())
	if err := metrics.addSample(0, line, producer.Add(-15*time.Millisecond), producer.Add(-10*time.Millisecond)); err != nil {
		t.Fatalf("add sample within clock-skew allowance: %v", err)
	}
	latencies, _, counts := metrics.snapshot()
	skewCount, maximumSkew := metrics.clockSkewSnapshot()
	if counts[0] != 1 || len(latencies[0]) != 1 || latencies[0][0] != 0 {
		t.Fatalf("sample metrics counts=%v latencies=%v, want one zero-clamped sample", counts, latencies[0])
	}
	if skewCount != 1 || maximumSkew != 15*time.Millisecond {
		t.Fatalf("clock skew count=%d maximum=%s, want 1 and 15ms", skewCount, maximumSkew)
	}
}

func TestP142AddSampleRejectsClockSkewAboveAllowance(t *testing.T) {
	metrics := newMetrics()
	producer := time.Unix(1_800_000_000, 0)
	line := p142SampleLine(0, producer.UnixNano())
	if err := metrics.addSample(0, line, producer.Add(-maximumClockSkew-time.Nanosecond), producer); err == nil {
		t.Fatal("sample beyond clock-skew allowance was accepted")
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
	startPath := "/tmp/P142 start/measure"
	readyPath := "/tmp/P142 start/worker 0 ready"
	script := loadScript(pythonPath, true, startPath, readyPath)
	if !bytes.Contains([]byte(script), []byte(shellQuote(pythonPath)+" -u -c ")) {
		t.Fatalf("load script does not quote Python path: %s", script)
	}
	if !bytes.Contains([]byte(script), []byte(fmt.Sprintf("range(%d)", slowSubscriberBurst/sampleRecordBytes))) {
		t.Fatalf("load script does not emit the configured startup burst")
	}
	if !bytes.Contains([]byte(script), []byte(fmt.Sprintf("range(%d)", sampleCount))) || !bytes.Contains([]byte(script), []byte("time.sleep(0.100)")) {
		t.Fatalf("load script does not emit the measured ten-minute workload")
	}
	if !bytes.Contains([]byte(script), []byte("while not os.path.exists(start_path)")) ||
		!bytes.Contains([]byte(script), []byte(fmt.Sprintf("ready_path = %q", readyPath))) ||
		!bytes.Contains([]byte(script), []byte(fmt.Sprintf("start_path = %q", startPath))) {
		t.Fatalf("load script does not wait at the synchronized workload start")
	}

	withoutBurst := loadScript(pythonPath, false, startPath, readyPath)
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
