package p142fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	p141fixture "remote-session-runner/src/internal/testfixture/p141"
)

const (
	activeSessionLimit     = 20
	runningCommandLimit    = 4
	sampleCount            = 6000
	sampleInterval         = 100 * time.Millisecond
	sampleRecordBytes      = 4096
	slowSubscriberBurst    = 1_310_720
	subscriberBufferSize   = 1 << 20
	commandCapacity        = 4096
	subscriberOverflowSlop = 16 * 1024
	loadContextTimeout     = 16 * time.Minute
	cleanupTimeout         = 90 * time.Second
	healthSampleInterval   = 30 * time.Second
	measuredLoadDuration   = time.Duration(sampleCount) * sampleInterval
	bufferSampleInterval   = 100 * time.Millisecond
	visibilityTarget       = 500 * time.Millisecond
)

type RuntimeController interface {
	CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error)
	StopSession(context.Context, store.SessionRecord) (bool, error)
}

type Options struct {
	Service       *execution.Service
	Authority     *store.AuthorityStore
	Runtime       RuntimeController
	Environment   domain.Environment
	Target        domain.ExecutionTarget
	Controller    domain.ControllerIdentity
	WorkspaceRoot string
	PythonPath    string
	ExpectedOS    string
	ExpectedUser  string
	ExpectedUID   string
	ExpectedHost  string
}

type worker struct {
	commandID domain.CommandID
	sessionID domain.SessionID
	burst     bool
	done      chan error
	finished  bool
}

type workerResult struct {
	index int
	err   error
}

type stableSnapshot struct {
	sessionReservations int
	commandSlots        int
	readySessions       int
	busySessions        int
	runningCommands     int
}

type subscriberRun struct {
	commandID domain.CommandID
	stream    *store.CommandEventSubscription
	done      chan error
	consuming bool
	maxBytes  atomic.Int64
}

type metrics struct {
	mu              sync.Mutex
	latencies       [][]time.Duration
	outputBytes     []int64
	nextSampleIndex []int
}

func newMetrics() *metrics {
	return &metrics{
		latencies:       make([][]time.Duration, runningCommandLimit),
		outputBytes:     make([]int64, runningCommandLimit),
		nextSampleIndex: make([]int, runningCommandLimit),
	}
}

func (m *metrics) addOutput(workerIndex int, event store.CommandEventRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outputBytes[workerIndex] += event.ByteCount
	return nil
}

func (m *metrics) snapshot() ([][]time.Duration, []int64, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latencies := make([][]time.Duration, len(m.latencies))
	for index := range m.latencies {
		latencies[index] = append([]time.Duration(nil), m.latencies[index]...)
	}
	return latencies, append([]int64(nil), m.outputBytes...), append([]int(nil), m.nextSampleIndex...)
}

// RunReferenceHostVisibilitySoak measures output visibility and stability on
// one actual host using an isolated authority and real host-process sessions.
func RunReferenceHostVisibilitySoak(t testing.TB, options Options) {
	t.Helper()
	if options.Service == nil || options.Authority == nil || options.Runtime == nil || options.WorkspaceRoot == "" {
		t.Fatal("P142 requires a service, authority, runtime controller, and isolated workspace root")
	}
	if !filepath.IsAbs(options.PythonPath) {
		t.Fatalf("P142 Python executable path %q must be absolute", options.PythonPath)
	}
	if runtime.GOOS != options.ExpectedOS {
		t.Fatalf("P142 host OS=%s, want %s", runtime.GOOS, options.ExpectedOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != options.ExpectedUser || current.Uid != options.ExpectedUID {
		t.Fatalf("P142 host account=%+v err=%v, want %s uid %s", current, err, options.ExpectedUser, options.ExpectedUID)
	}
	host, err := os.Hostname()
	if err != nil || host != options.ExpectedHost {
		t.Fatalf("P142 host name=%q err=%v, want %s", host, err, options.ExpectedHost)
	}
	limits := options.Environment.ServiceLimits()
	if limits.ActiveSessionsPerHost != activeSessionLimit || limits.RunningCommandsPerHost != runningCommandLimit || limits.SubscriberBufferBytes != subscriberBufferSize {
		t.Fatalf("P142 environment limits=%+v, want 20 sessions, four commands, and a 1 MiB subscriber", limits)
	}

	ctx, cancel := context.WithTimeout(context.Background(), loadContextTimeout)
	defer cancel()
	var sessions []domain.SessionID
	var workers []*worker
	var subscriptions []*subscriberRun
	cleaned := make(map[domain.SessionID]bool)
	var sampler p141fixture.MemorySampler
	cleanup := func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer stop()
		for _, sessionID := range sessions {
			session, readErr := options.Authority.GetSession(cleanupCtx, sessionID)
			if readErr != nil {
				continue
			}
			commands, listErr := options.Authority.ListSessionCommands(cleanupCtx, sessionID)
			if listErr != nil {
				continue
			}
			for _, command := range commands {
				if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
					continue
				}
				_, _ = options.Runtime.CancelCommand(cleanupCtx, execution.RuntimeCommandRequest{Session: session, Command: command})
			}
		}
		for _, currentWorker := range workers {
			if currentWorker.finished {
				continue
			}
			select {
			case <-currentWorker.done:
				currentWorker.finished = true
			case <-cleanupCtx.Done():
				t.Errorf("P142 command worker %s did not stop before cleanup: %v", currentWorker.commandID, cleanupCtx.Err())
			}
		}
		for _, subscription := range subscriptions {
			subscription.stream.Close()
		}
		for _, subscription := range subscriptions {
			if !subscription.consuming {
				continue
			}
			select {
			case <-subscription.done:
			case <-cleanupCtx.Done():
				t.Errorf("P142 event consumer for %s did not stop before cleanup: %v", subscription.commandID, cleanupCtx.Err())
			}
		}
		for _, currentSession := range sessions {
			if cleaned[currentSession] {
				continue
			}
			session, readErr := options.Authority.GetSession(cleanupCtx, currentSession)
			if readErr != nil {
				continue
			}
			confirmed, stopErr := options.Runtime.StopSession(cleanupCtx, session)
			if stopErr != nil || !confirmed {
				t.Errorf("stop P142 fixture session %s: confirmed=%t err=%v", currentSession, confirmed, stopErr)
			}
		}
		if sampler != nil {
			if _, sampleErr := sampler.Stop(); sampleErr != nil {
				t.Errorf("stop P142 memory sampler: %v", sampleErr)
			}
		}
		markers, markerErr := os.ReadDir(filepath.Join(options.WorkspaceRoot, ".runner-runtime-ownership"))
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			t.Errorf("read P142 ownership markers after cleanup: %v", markerErr)
		} else if len(markers) != 0 {
			t.Errorf("P142 cleanup left %d runtime ownership markers", len(markers))
		}
	}
	defer cleanup()

	sampler, err = p141fixture.StartMemorySampler(options.WorkspaceRoot, time.Second)
	if err != nil {
		t.Fatalf("start P142 reference-host memory sampler: %v", err)
	}
	if _, err := os.Stat(options.PythonPath); err != nil {
		t.Fatalf("stat selected P142 Python executable: %v", err)
	}
	if output, versionErr := exec.CommandContext(ctx, options.PythonPath, "--version").Output(); versionErr != nil || !strings.HasPrefix(strings.TrimSpace(string(output)), "Python ") {
		t.Fatalf("check P142 Python executable version=%q err=%v", output, versionErr)
	}

	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	prefix := "p142-" + stamp
	for index := 0; index < activeSessionLimit; index++ {
		sessionID := domain.SessionID(fmt.Sprintf("%s-s%02d", prefix, index))
		created, createErr := options.Service.CreateSession(ctx, createRequest(t, options, sessionID))
		if createErr != nil || created.Session.State != domain.SessionStateReady {
			t.Fatalf("create P142 session %s = %+v err=%v", sessionID, created.Session, createErr)
		}
		sessions = append(sessions, sessionID)
	}
	if reservations, countErr := options.Authority.CountLiveSessionReservations(ctx); countErr != nil || reservations != activeSessionLimit {
		t.Fatalf("P142 session reservations=%d err=%v, want %d", reservations, countErr, activeSessionLimit)
	}

	for index := 0; index < runningCommandLimit; index++ {
		commandID := domain.CommandID(fmt.Sprintf("%s-c%02d", prefix, index))
		currentWorker := &worker{commandID: commandID, sessionID: sessions[index], burst: index == 0, done: make(chan error, 1)}
		workers = append(workers, currentWorker)
		if _, submitErr := options.Service.AcceptCommand(ctx, submitRequest(t, options, currentWorker)); submitErr != nil {
			t.Fatalf("accept P142 command %s: %v", commandID, submitErr)
		}
		subscription, subscribeErr := options.Service.SubscribeCommandEvents(ctx, commandID, options.Controller, 0, commandCapacity)
		if subscribeErr != nil {
			t.Fatalf("subscribe P142 output consumer for %s: %v", commandID, subscribeErr)
		}
		subscriptions = append(subscriptions, &subscriberRun{commandID: commandID, stream: subscription, done: make(chan error, 1), consuming: true})
	}
	slowSubscription, err := options.Service.SubscribeCommandEvents(ctx, workers[0].commandID, options.Controller, 0, commandCapacity)
	if err != nil {
		t.Fatalf("subscribe P142 unread output consumer: %v", err)
	}
	slow := &subscriberRun{commandID: workers[0].commandID, stream: slowSubscription, done: make(chan error, 1)}
	subscriptions = append(subscriptions, slow)

	metrics := newMetrics()
	consumerErrors := make(chan error, runningCommandLimit)
	for index, subscription := range subscriptions[:runningCommandLimit] {
		go func(workerIndex int, current *subscriberRun) {
			consumerErrors <- consumeEvents(ctx, options.Authority, current, workerIndex, metrics)
		}(index, subscription)
	}
	workerResults := make(chan workerResult, runningCommandLimit)
	for index, currentWorker := range workers {
		go func(workerIndex int, value *worker) {
			_, runErr := options.Service.ResumeCommand(ctx, value.commandID, options.Controller)
			value.done <- runErr
			workerResults <- workerResult{index: workerIndex, err: runErr}
		}(index, currentWorker)
		waitCommandState(t, ctx, options.Authority, currentWorker.commandID, domain.CommandStateRunning)
	}
	initialState, err := verifyStableState(ctx, options.Authority, sessions, workers)
	if err != nil {
		t.Fatalf("P142 initial stable-load state: %v", err)
	}
	logStabilitySample(t, 0, initialState)
	stableChecks := 1
	loadStarted := time.Now()
	healthTicker := time.NewTicker(healthSampleInterval)
	defer healthTicker.Stop()
	bufferTicker := time.NewTicker(bufferSampleInterval)
	defer bufferTicker.Stop()
	slowOverflowTimer := time.NewTimer(30 * time.Second)
	defer slowOverflowTimer.Stop()
	slowOverflowDeadline := slowOverflowTimer.C
	var slowOverflow error
	var slowOverflowBufferedBytes int64
	slowErrors := slow.stream.Errors()
	completedWorkers := 0
	completedConsumers := 0
	maxSlowBytes := int64(0)
	for completedWorkers < runningCommandLimit {
		select {
		case result := <-workerResults:
			workers[result.index].finished = true
			completedWorkers++
			if result.err != nil {
				t.Fatalf("P142 command %s result: %v", workers[result.index].commandID, result.err)
			}
		case consumerErr := <-consumerErrors:
			completedConsumers++
			if consumerErr != nil {
				t.Fatalf("P142 persisted event consumer failed: %v", consumerErr)
			}
		case overflow, open := <-slowErrors:
			if !open || !errors.Is(overflow, store.ErrSubscriberOverflow) {
				t.Fatalf("P142 slow subscriber error=%v open=%t, want byte-bounded overflow", overflow, open)
			}
			slowOverflow = overflow
			slowOverflowBufferedBytes = slow.stream.BufferedPayloadBytes()
			updateMaximumValue(&maxSlowBytes, slowOverflowBufferedBytes)
			updateMaximum(&slow.maxBytes, slowOverflowBufferedBytes)
			slowErrors = nil
		case <-slowOverflowDeadline:
			if slowOverflow == nil {
				t.Fatal("P142 unread subscriber did not overflow within 30 seconds of the startup burst")
			}
			slowOverflowTimer.Stop()
			slowOverflowDeadline = nil
		case <-bufferTicker.C:
			for _, subscription := range subscriptions {
				buffered := subscription.stream.BufferedPayloadBytes()
				updateMaximum(&subscription.maxBytes, buffered)
				if buffered > subscriberBufferSize {
					t.Fatalf("P142 subscriber %s buffered %d bytes over the %d-byte policy", subscription.commandID, buffered, subscriberBufferSize)
				}
			}
			updateMaximumValue(&maxSlowBytes, slow.stream.BufferedPayloadBytes())
		case <-healthTicker.C:
			if time.Since(loadStarted) >= measuredLoadDuration-healthSampleInterval {
				continue
			}
			state, err := verifyStableState(ctx, options.Authority, sessions, workers)
			if err != nil {
				t.Fatalf("P142 stability sample at %s: %v", time.Since(loadStarted).Round(time.Second), err)
			}
			logStabilitySample(t, time.Since(loadStarted), state)
			stableChecks++
		case <-ctx.Done():
			t.Fatalf("P142 ten-minute load ended early after %s: %v", time.Since(loadStarted).Round(time.Second), ctx.Err())
		}
	}
	if slowOverflow == nil {
		t.Fatal("P142 slow subscriber did not overflow at the configured byte bound")
	}
	if slowOverflowBufferedBytes < subscriberBufferSize-subscriberOverflowSlop {
		t.Fatalf("P142 slow subscriber overflowed at %d buffered bytes, too far below the byte cap %d to demonstrate byte-triggered overflow", slowOverflowBufferedBytes, subscriberBufferSize)
	}
	if sequence := slow.stream.LastSequence(); sequence >= commandCapacity {
		t.Fatalf("P142 slow subscriber last queued sequence=%d reached event-count capacity %d before byte overflow", sequence, commandCapacity)
	}
	for completedConsumers < runningCommandLimit {
		select {
		case consumerErr := <-consumerErrors:
			completedConsumers++
			if consumerErr != nil {
				t.Fatalf("P142 persisted event consumer failed: %v", consumerErr)
			}
		case <-ctx.Done():
			t.Fatalf("wait for P142 event consumers: %v", ctx.Err())
		}
	}
	latencies, outputBytes, sampleCounts := metrics.snapshot()
	var allLatencies []time.Duration
	for index, samples := range latencies {
		if sampleCounts[index] != sampleCount || len(samples) != sampleCount {
			t.Fatalf("P142 worker %d persisted sample count=%d latencies=%d, want %d", index, sampleCounts[index], len(samples), sampleCount)
		}
		wantOutputBytes := int64(sampleCount * sampleRecordBytes)
		if index == 0 {
			wantOutputBytes += slowSubscriberBurst
		}
		if outputBytes[index] != wantOutputBytes {
			t.Fatalf("P142 worker %d output bytes=%d, want %d", index, outputBytes[index], wantOutputBytes)
		}
		allLatencies = append(allLatencies, samples...)
	}
	if len(allLatencies) != runningCommandLimit*sampleCount {
		t.Fatalf("P142 total visibility samples=%d, want %d", len(allLatencies), runningCommandLimit*sampleCount)
	}
	sort.Slice(allLatencies, func(i, j int) bool { return allLatencies[i] < allLatencies[j] })
	misses := 0
	for _, latency := range allLatencies {
		if latency > visibilityTarget {
			misses++
		}
	}
	p50 := percentile(allLatencies, 50)
	p95 := percentile(allLatencies, 95)
	p99 := percentile(allLatencies, 99)
	maximum := allLatencies[len(allLatencies)-1]
	if p99 > visibilityTarget {
		t.Fatalf("P142 persisted-output p99=%s exceeds the %s target (%d samples over target)", p99, visibilityTarget, misses)
	}
	if stableChecks < 19 {
		t.Fatalf("P142 completed only %d 30-second stability samples, want at least 19", stableChecks)
	}

	if current := slow.stream.BufferedPayloadBytes(); current > subscriberBufferSize || maxSlowBytes > subscriberBufferSize {
		t.Fatalf("P142 slow subscriber byte high-water=%d current=%d limit=%d", maxSlowBytes, current, subscriberBufferSize)
	}
	var slowCursor int64
	for event := range slow.stream.Events() {
		slow.stream.Acknowledge(event)
		slowCursor = event.Sequence
	}
	if slowCursor <= 0 || slowCursor >= commandCapacity || slow.stream.BufferedPayloadBytes() != 0 {
		t.Fatalf("P142 slow subscriber prefix cursor=%d buffered=%d, want a resumable prefix and zero after acknowledgement", slowCursor, slow.stream.BufferedPayloadBytes())
	}
	slowCommand, err := options.Authority.GetCommand(ctx, slow.commandID)
	if err != nil || slowCommand.FinalEventSequence == nil {
		t.Fatalf("read P142 slow-subscriber command terminal cursor=%v err=%v", slowCommand.FinalEventSequence, err)
	}
	replay, err := options.Authority.ReplayCommandEvents(ctx, slow.commandID, slowCursor)
	if err != nil {
		t.Fatalf("replay P142 slow-subscriber suffix after %d: %v", slowCursor, err)
	}
	if len(replay) == 0 || replay[0].Sequence != slowCursor+1 || replay[len(replay)-1].Sequence != *slowCommand.FinalEventSequence {
		t.Fatalf("P142 durable suffix after slow cursor %d has %d events, terminal=%d", slowCursor, len(replay), *slowCommand.FinalEventSequence)
	}
	for index, event := range replay {
		if event.Sequence != slowCursor+1+int64(index) {
			t.Fatalf("P142 durable slow-subscriber suffix sequence=%d at offset=%d", event.Sequence, index)
		}
	}

	stats, err := sampler.Stop()
	sampler = nil
	if err != nil {
		t.Fatalf("sample P142 host memory: %v", err)
	}
	if stats.Samples == 0 || stats.PeakFixtureRSSBytes <= 0 || stats.BaselineAvailableBytes <= 0 || stats.PeakFixtureRSSBytes > stats.BaselineAvailableBytes {
		t.Fatalf("P142 memory evidence is incomplete or exceeds available memory: %+v", stats)
	}

	for _, currentWorker := range workers {
		command, commandErr := options.Authority.GetCommand(ctx, currentWorker.commandID)
		if commandErr != nil || command.State != domain.CommandStateSucceeded || !command.OutputComplete || command.OutputTruncated || command.FinalEventSequence == nil {
			t.Fatalf("P142 command %s=%+v err=%v, want complete untruncated success", currentWorker.commandID, command, commandErr)
		}
	}
	if slots, countErr := options.Authority.CountLiveCommandSlots(ctx); countErr != nil || slots != 0 {
		t.Fatalf("P142 live command slots after completed workload=%d err=%v, want zero", slots, countErr)
	}

	var normalSubscriberPeaks []int64
	for _, subscription := range subscriptions[:runningCommandLimit] {
		normalSubscriberPeaks = append(normalSubscriberPeaks, subscription.maxBytes.Load())
	}
	t.Logf("machine=%s os=%s account=%s; sessions=%d; simultaneous_commands=%d; load_duration=%s; records_per_command=%d; total_records=%d; record_bytes=%d; persisted_output_bytes=%d; visibility_p50=%s; visibility_p95=%s; visibility_p99=%s; visibility_max=%s; samples_over_500ms=%d; normal_subscriber_peak_bytes=%v; slow_subscriber_peak_bytes=%d; slow_subscriber_bytes_at_overflow=%d; slow_subscriber_limit_bytes=%d; slow_replay_cursor=%d; stable_state_samples=%d; memory_samples=%d; baseline_available_bytes=%d; peak_fixture_rss_bytes=%d; available_after_bytes=%d; live_command_slots_after_load=0",
		host, runtime.GOOS, current.Username, activeSessionLimit, runningCommandLimit, time.Duration(sampleCount)*sampleInterval,
		sampleCount, len(allLatencies), sampleRecordBytes, sum(outputBytes), p50, p95, p99, maximum, misses,
		normalSubscriberPeaks, maxSlowBytes, slowOverflowBufferedBytes, subscriberBufferSize, slowCursor, stableChecks, stats.Samples, stats.BaselineAvailableBytes,
		stats.PeakFixtureRSSBytes, stats.AvailableAfterBytes)

	for _, sessionID := range sessions {
		if _, closeErr := options.Service.CloseSession(ctx, closeRequest(t, options, sessionID, prefix)); closeErr != nil {
			t.Fatalf("close P142 fixture session %s: %v", sessionID, closeErr)
		}
		cleaned[sessionID] = true
	}
	if slots, countErr := options.Authority.CountLiveCommandSlots(ctx); countErr != nil || slots != 0 {
		t.Fatalf("P142 live command slots after cleanup=%d err=%v, want zero", slots, countErr)
	}
	if reservations, countErr := options.Authority.CountLiveSessionReservations(ctx); countErr != nil || reservations != 0 {
		t.Fatalf("P142 session reservations after cleanup=%d err=%v, want zero", reservations, countErr)
	}
	markers, err := os.ReadDir(filepath.Join(options.WorkspaceRoot, ".runner-runtime-ownership"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read P142 ownership markers after teardown: %v", err)
	}
	if len(markers) != 0 {
		t.Fatalf("P142 left %d runtime ownership markers after teardown", len(markers))
	}
}

func consumeEvents(ctx context.Context, authority *store.AuthorityStore, subscription *subscriberRun, workerIndex int, observed *metrics) error {
	defer close(subscription.done)
	var lastSequence int64
	var pending []byte
	for event := range subscription.stream.Events() {
		if event.Sequence != lastSequence+1 {
			return fmt.Errorf("P142 command %s event sequence=%d after %d", subscription.commandID, event.Sequence, lastSequence)
		}
		persisted, err := authority.ReplayCommandEvents(ctx, subscription.commandID, event.Sequence-1)
		if err != nil {
			return fmt.Errorf("read persisted P142 command %s event %d: %w", subscription.commandID, event.Sequence, err)
		}
		found := false
		for _, record := range persisted {
			if record.Sequence == event.Sequence {
				if record.Type != event.Type || record.ByteCount != event.ByteCount || !bytes.Equal(record.Payload, event.Payload) {
					return fmt.Errorf("P142 delivered event %d for %s differs from its persisted record", event.Sequence, subscription.commandID)
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("P142 delivered event %d for %s is not visible in durable replay", event.Sequence, subscription.commandID)
		}
		observedAt := time.Now()
		if event.Type == "stdout" || event.Type == "stderr" {
			pending = append(pending, event.Payload...)
			for {
				lineEnd := bytes.IndexByte(pending, '\n')
				if lineEnd < 0 {
					break
				}
				line := append([]byte(nil), pending[:lineEnd]...)
				pending = pending[lineEnd+1:]
				if err := observed.addSample(workerIndex, line, observedAt); err != nil {
					return err
				}
			}
			if err := observed.addOutput(workerIndex, event); err != nil {
				return err
			}
		}
		lastSequence = event.Sequence
		updateMaximum(&subscription.maxBytes, subscription.stream.BufferedPayloadBytes())
		subscription.stream.Acknowledge(event)
		updateMaximum(&subscription.maxBytes, subscription.stream.BufferedPayloadBytes())
		if isTerminalEvent(event.Type) {
			if len(pending) != 0 {
				return fmt.Errorf("P142 command %s ended with %d unterminated output bytes", subscription.commandID, len(pending))
			}
			return nil
		}
	}
	return fmt.Errorf("P142 command %s event subscription closed before a terminal event", subscription.commandID)
}

func (m *metrics) addSample(workerIndex int, line []byte, observedAt time.Time) error {
	if !bytes.HasPrefix(line, []byte("P142|")) {
		return nil
	}
	if len(line) != sampleRecordBytes-1 {
		return fmt.Errorf("P142 sample record has %d payload bytes, want %d", len(line), sampleRecordBytes-1)
	}
	first := bytes.IndexByte(line, '|')
	secondRelative := bytes.IndexByte(line[first+1:], '|')
	if first < 0 || secondRelative < 0 {
		return fmt.Errorf("P142 sample record has malformed separators")
	}
	second := first + 1 + secondRelative
	thirdRelative := bytes.IndexByte(line[second+1:], '|')
	if thirdRelative < 0 {
		return fmt.Errorf("P142 sample record has no timestamp")
	}
	third := second + 1 + thirdRelative
	index, err := strconv.Atoi(string(line[first+1 : second]))
	if err != nil {
		return fmt.Errorf("parse P142 sample index: %w", err)
	}
	stamp, err := strconv.ParseInt(string(line[second+1:third]), 10, 64)
	if err != nil {
		return fmt.Errorf("parse P142 producer timestamp: %w", err)
	}
	latency := observedAt.Sub(time.Unix(0, stamp))
	if latency < 0 {
		return fmt.Errorf("P142 producer timestamp is in the future by %s", -latency)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if index != m.nextSampleIndex[workerIndex] {
		return fmt.Errorf("P142 worker %d sample index=%d, want %d", workerIndex, index, m.nextSampleIndex[workerIndex])
	}
	m.nextSampleIndex[workerIndex]++
	m.latencies[workerIndex] = append(m.latencies[workerIndex], latency)
	return nil
}

func isTerminalEvent(kind string) bool {
	switch kind {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func verifyStableState(ctx context.Context, authority *store.AuthorityStore, sessions []domain.SessionID, workers []*worker) (stableSnapshot, error) {
	snapshot := stableSnapshot{}
	reservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil || reservations != activeSessionLimit {
		return snapshot, fmt.Errorf("live session reservations=%d err=%v, want %d", reservations, err, activeSessionLimit)
	}
	snapshot.sessionReservations = reservations
	slots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil || slots != runningCommandLimit {
		return snapshot, fmt.Errorf("live command slots=%d err=%v, want %d", slots, err, runningCommandLimit)
	}
	snapshot.commandSlots = slots
	active := make(map[domain.SessionID]struct{}, len(workers))
	for _, currentWorker := range workers {
		active[currentWorker.sessionID] = struct{}{}
		command, readErr := authority.GetCommand(ctx, currentWorker.commandID)
		if readErr != nil || command.State != domain.CommandStateRunning {
			return snapshot, fmt.Errorf("active command %s state=%s err=%v, want running", currentWorker.commandID, command.State, readErr)
		}
		snapshot.runningCommands++
	}
	for _, sessionID := range sessions {
		session, readErr := authority.GetSession(ctx, sessionID)
		if readErr != nil {
			return snapshot, fmt.Errorf("read session %s: %w", sessionID, readErr)
		}
		want := domain.SessionStateReady
		if _, ok := active[sessionID]; ok {
			want = domain.SessionStateBusy
		}
		if session.State != want {
			return snapshot, fmt.Errorf("session %s state=%s, want %s", sessionID, session.State, want)
		}
		if session.State == domain.SessionStateReady {
			snapshot.readySessions++
		} else {
			snapshot.busySessions++
		}
	}
	return snapshot, nil
}

func logStabilitySample(t testing.TB, elapsed time.Duration, snapshot stableSnapshot) {
	t.Helper()
	t.Logf("P142 stability_sample_elapsed=%s session_reservations=%d command_slots=%d ready_sessions=%d busy_sessions=%d running_commands=%d",
		elapsed.Round(time.Second), snapshot.sessionReservations, snapshot.commandSlots, snapshot.readySessions, snapshot.busySessions, snapshot.runningCommands)
}

func waitCommandState(t testing.TB, ctx context.Context, authority *store.AuthorityStore, commandID domain.CommandID, expected domain.CommandState) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		command, err := authority.GetCommand(ctx, commandID)
		if err == nil && command.State == expected {
			return
		}
		if err != nil && !errors.Is(err, store.ErrCommandNotFound) {
			t.Fatalf("read P142 command %s: %v", commandID, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for P142 command %s state %s: %v", commandID, expected, ctx.Err())
		case <-deadline.C:
			t.Fatalf("P142 command %s did not reach %s", commandID, expected)
		case <-ticker.C:
		}
	}
}

func createRequest(t testing.TB, options Options, sessionID domain.SessionID) execution.CreateSessionRequest {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"environment":      options.Environment.Name(),
		"execution_target": map[string]string{"kind": string(options.Target.Kind()), "profile": options.Target.Profile()},
		"session_id":       string(sessionID), "source": map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.CreateSessionRequest{
		SessionID: sessionID, IdempotencyKey: "create-" + string(sessionID), RequestHash: hash,
		Environment: options.Environment.Name(), Target: options.Target, Controller: options.Controller,
		Source: domain.NewEmptySource(), MaxActiveSessions: activeSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func submitRequest(t testing.TB, options Options, currentWorker *worker) execution.SubmitCommandRequest {
	t.Helper()
	script := loadScript(options.PythonPath, currentWorker.burst)
	encoded, err := json.Marshal(map[string]string{"script": script, "session_id": string(currentWorker.sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.SubmitCommandRequest{
		CommandID: currentWorker.commandID, SessionID: currentWorker.sessionID, Controller: options.Controller,
		IdempotencyKey: "submit-" + string(currentWorker.commandID), RequestHash: hash, Script: script,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func closeRequest(t testing.TB, options Options, sessionID domain.SessionID, prefix string) execution.CloseSessionRequest {
	t.Helper()
	policy := "p142-load-cleanup"
	encoded, err := json.Marshal(map[string]string{"policy": policy, "session_id": string(sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("close_session", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.CloseSessionRequest{
		SessionID: sessionID, Controller: options.Controller,
		IdempotencyKey: prefix + "-close-" + string(sessionID), RequestHash: hash,
		Policy: policy, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func loadScript(pythonPath string, burst bool) string {
	burstSource := ""
	if burst {
		burstSource = fmt.Sprintf("burst = b'B' * (%d - 1) + b'\\n'\nfor _ in range(%d):\n    write_all(burst)\n", sampleRecordBytes, slowSubscriberBurst/sampleRecordBytes)
	}
	program := fmt.Sprintf("import os, time\ndef write_all(data):\n    offset = 0\n    while offset < len(data):\n        offset += os.write(1, data[offset:])\n%sfor i in range(%d):\n    prefix = f'P142|{i}|{time.time_ns()}|'.encode()\n    record = prefix + b'x' * (%d - len(prefix) - 1) + b'\\n'\n    write_all(record)\n    time.sleep(%.3f)\n", burstSource, sampleCount, sampleRecordBytes, sampleInterval.Seconds())
	return shellQuote(pythonPath) + " -u -c " + shellQuote(program) + "\n"
}

func percentile(values []time.Duration, percent int) time.Duration {
	if len(values) == 0 || percent < 1 || percent > 100 {
		return 0
	}
	rank := (len(values)*percent+99)/100 - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(values) {
		rank = len(values) - 1
	}
	return values[rank]
}

func updateMaximum(target *atomic.Int64, value int64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func updateMaximumValue(target *int64, value int64) {
	if value > *target {
		*target = value
	}
}

func sum(values []int64) int64 {
	var result int64
	for _, value := range values {
		result += value
	}
	return result
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
