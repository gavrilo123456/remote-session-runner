package testfixture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// BarrierPoint is a stable name for a crash boundary exercised by a test.
type BarrierPoint string

const (
	BarrierMacAPIAfterIntentCommit    BarrierPoint = "mac_api.after_intent_commit"
	BarrierRouterAfterLeaseCommit     BarrierPoint = "router.after_delivery_lease_commit"
	BarrierRouterAfterUncertainCommit BarrierPoint = "router.after_uncertain_intent_commit"
	BarrierExecutorAfterStartCommit   BarrierPoint = "executor.after_command_started_commit"
	BarrierAgentAfterBashSpawn        BarrierPoint = "agent.after_bash_spawn"
	BarrierBashAfterScriptSource      BarrierPoint = "bash.after_script_source"
	phaseBarrierFDEnv                              = "RSR_PHASE_BARRIER_FD"
	phaseBarrierContinue                           = "continue"
	phaseBarrierMessageLimit                       = 64 * 1024
	phaseBarrierOutputLimit                        = 64 * 1024
	phaseBarrierWaitTimeout                        = 5 * time.Second
)

// NamedPhaseBarriers returns the P133 crash points in deterministic plan order.
func NamedPhaseBarriers() []BarrierPoint {
	return []BarrierPoint{
		BarrierMacAPIAfterIntentCommit,
		BarrierRouterAfterLeaseCommit,
		BarrierRouterAfterUncertainCommit,
		BarrierExecutorAfterStartCommit,
		BarrierAgentAfterBashSpawn,
		BarrierBashAfterScriptSource,
	}
}

func validBarrierPoint(point BarrierPoint) bool {
	for _, named := range NamedPhaseBarriers() {
		if point == named {
			return true
		}
	}
	return false
}

type phaseBarrierMessage struct {
	Type   string          `json:"type"`
	Point  BarrierPoint    `json:"point,omitempty"`
	PID    int             `json:"pid,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type phaseBarrierReply struct {
	Type  string       `json:"type"`
	Point BarrierPoint `json:"point"`
}

// OpenPhaseBarrierReporter returns the child-process event pipe installed by
// StartBarrierProcess. The caller must close the returned file.
func OpenPhaseBarrierReporter() (*os.File, error) {
	value := os.Getenv(phaseBarrierFDEnv)
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return nil, fmt.Errorf("invalid or missing %s", phaseBarrierFDEnv)
	}
	file := os.NewFile(uintptr(fd), "phase-barrier-events")
	if file == nil {
		return nil, errors.New("phase-barrier event descriptor is unavailable")
	}
	return file, nil
}

// WaitAtPhaseBarrier publishes a named child-process barrier and blocks until
// the parent explicitly releases that same point. A killed child leaves the
// parent free to restart it without releasing the barrier.
func WaitAtPhaseBarrier(input io.Reader, reporter io.Writer, point BarrierPoint) error {
	if !validBarrierPoint(point) {
		return fmt.Errorf("unknown phase barrier %q", point)
	}
	if err := writeBarrierMessage(reporter, phaseBarrierMessage{
		Type:  "barrier",
		Point: point,
		PID:   os.Getpid(),
	}); err != nil {
		return fmt.Errorf("publish phase barrier %q: %w", point, err)
	}

	decoder := json.NewDecoder(io.LimitReader(input, phaseBarrierMessageLimit))
	var reply phaseBarrierReply
	if err := decoder.Decode(&reply); err != nil {
		return fmt.Errorf("wait for phase barrier %q release: %w", point, err)
	}
	if reply.Type != phaseBarrierContinue || reply.Point != point {
		return fmt.Errorf("phase barrier %q received unexpected release %+v", point, reply)
	}
	return nil
}

// PublishPhaseResult sends a JSON result over the child-process event pipe.
func PublishPhaseResult(reporter io.Writer, result any) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode phase result: %w", err)
	}
	return writeBarrierMessage(reporter, phaseBarrierMessage{Type: "result", Result: encoded})
}

func writeBarrierMessage(writer io.Writer, message phaseBarrierMessage) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > phaseBarrierMessageLimit {
		return fmt.Errorf("phase event is %d bytes, limit is %d", len(encoded), phaseBarrierMessageLimit)
	}
	for len(encoded) > 0 {
		written, err := writer.Write(encoded)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[written:]
	}
	return nil
}

// PhaseHarness starts, kills, and restarts one deterministic helper process.
// The process factory is called for each start and must point at task-owned
// fixtures (normally beneath Root.Path()).
type PhaseHarness struct {
	testingTB testing.TB
	factory   func() *exec.Cmd
	process   *BarrierProcess
}

// NewPhaseHarness creates a process harness. Start must be called before use.
func NewPhaseHarness(t testing.TB, factory func() *exec.Cmd) *PhaseHarness {
	t.Helper()
	if factory == nil {
		t.Fatal("phase harness requires a process factory")
	}
	return &PhaseHarness{testingTB: t, factory: factory}
}

// Start launches the initial child process.
func (h *PhaseHarness) Start() (*BarrierProcess, error) {
	h.testingTB.Helper()
	if h.process != nil {
		return nil, errors.New("phase harness has already started; use Restart after stopping its process")
	}
	return h.start()
}

// Kill stops the current child with SIGKILL and waits for it to be reaped.
func (h *PhaseHarness) Kill() error {
	h.testingTB.Helper()
	if h.process == nil {
		return errors.New("phase harness has not started")
	}
	return h.process.KillAndWait()
}

// Restart starts a fresh child after the prior child has exited.
func (h *PhaseHarness) Restart() (*BarrierProcess, error) {
	h.testingTB.Helper()
	if h.process == nil {
		return nil, errors.New("phase harness has not started")
	}
	if !h.process.Exited() {
		return nil, errors.New("phase harness cannot restart while its child is still running")
	}
	return h.start()
}

func (h *PhaseHarness) start() (*BarrierProcess, error) {
	process, err := StartBarrierProcess(h.testingTB, h.factory())
	if err != nil {
		return nil, err
	}
	h.process = process
	return process, nil
}

// BarrierProcess represents one child started by a PhaseHarness.
type BarrierProcess struct {
	cmd         *exec.Cmd
	control     io.WriteCloser
	reader      *os.File
	events      chan phaseBarrierMessage
	readerDone  chan struct{}
	readerErr   error
	readerMu    sync.Mutex
	processDone chan struct{}
	processErr  error
	processMu   sync.Mutex
	output      *limitedOutput
	closeOnce   sync.Once
}

// StartBarrierProcess starts cmd with a dedicated JSON event pipe and a
// parent-controlled stdin channel. It registers cleanup so a failed test does
// not leave the fixture process running.
func StartBarrierProcess(t testing.TB, cmd *exec.Cmd) (*BarrierProcess, error) {
	t.Helper()
	if cmd == nil || cmd.Path == "" {
		return nil, errors.New("phase barrier requires a command")
	}
	if cmd.Process != nil {
		return nil, errors.New("phase barrier command is already running")
	}
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create phase event pipe: %w", err)
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, writeEnd)
	cmd.Env = setEnvironment(cmd.Env, phaseBarrierFDEnv, strconv.Itoa(fd))
	control, err := cmd.StdinPipe()
	if err != nil {
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return nil, fmt.Errorf("create phase control pipe: %w", err)
	}

	output := &limitedOutput{limit: phaseBarrierOutputLimit}
	if cmd.Stdout == nil {
		cmd.Stdout = output
	}
	if cmd.Stderr == nil {
		cmd.Stderr = output
	}
	if err := cmd.Start(); err != nil {
		_ = control.Close()
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return nil, fmt.Errorf("start phase process: %w", err)
	}
	_ = writeEnd.Close()

	process := &BarrierProcess{
		cmd:         cmd,
		control:     control,
		reader:      readEnd,
		events:      make(chan phaseBarrierMessage, 16),
		readerDone:  make(chan struct{}),
		processDone: make(chan struct{}),
		output:      output,
	}
	go process.readEvents()
	go process.waitForExit()
	t.Cleanup(func() { process.cleanup(t) })
	return process, nil
}

func setEnvironment(environment []string, key, value string) []string {
	if environment == nil {
		environment = os.Environ()
	}
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name != key {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

func (p *BarrierProcess) readEvents() {
	defer close(p.readerDone)
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 1024), phaseBarrierMessageLimit)
	for scanner.Scan() {
		var message phaseBarrierMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			p.readerMu.Lock()
			p.readerErr = fmt.Errorf("decode phase event: %w", err)
			p.readerMu.Unlock()
			return
		}
		select {
		case p.events <- message:
		default:
			p.readerMu.Lock()
			p.readerErr = errors.New("phase event buffer exceeded 16 messages")
			p.readerMu.Unlock()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		p.readerMu.Lock()
		p.readerErr = fmt.Errorf("read phase event pipe: %w", err)
		p.readerMu.Unlock()
	}
}

func (p *BarrierProcess) waitForExit() {
	err := p.cmd.Wait()
	p.processMu.Lock()
	p.processErr = err
	p.processMu.Unlock()
	close(p.processDone)
}

// WaitForBarrier waits for exactly point, rejecting early exit, malformed
// events, mismatched names, and events attributed to another process.
func (p *BarrierProcess) WaitForBarrier(ctx context.Context, point BarrierPoint) error {
	if !validBarrierPoint(point) {
		return fmt.Errorf("unknown phase barrier %q", point)
	}
	message, err := p.nextEvent(ctx)
	if err != nil {
		return err
	}
	if message.Type != "barrier" || message.Point != point {
		return fmt.Errorf("phase event = type %q point %q, want barrier %q", message.Type, message.Point, point)
	}
	if message.PID != p.cmd.Process.Pid {
		return fmt.Errorf("phase barrier %q came from PID %d, want child PID %d", point, message.PID, p.cmd.Process.Pid)
	}
	return nil
}

// WaitForResult receives one JSON result from the child process.
func (p *BarrierProcess) WaitForResult(ctx context.Context) (json.RawMessage, error) {
	message, err := p.nextEvent(ctx)
	if err != nil {
		return nil, err
	}
	if message.Type != "result" || len(message.Result) == 0 || !json.Valid(message.Result) {
		return nil, fmt.Errorf("unexpected phase result message type %q", message.Type)
	}
	return append(json.RawMessage(nil), message.Result...), nil
}

func (p *BarrierProcess) nextEvent(ctx context.Context) (phaseBarrierMessage, error) {
	select {
	case message := <-p.events:
		return message, nil
	default:
	}
	select {
	case message := <-p.events:
		return message, nil
	case <-p.processDone:
		select {
		case message := <-p.events:
			return message, nil
		default:
		}
		select {
		case <-p.readerDone:
			select {
			case message := <-p.events:
				return message, nil
			default:
			}
		case <-ctx.Done():
			return phaseBarrierMessage{}, fmt.Errorf("wait for phase event reader: %w", ctx.Err())
		}
		return phaseBarrierMessage{}, fmt.Errorf("phase child exited before sending an event: %s", p.failureDetail())
	case <-p.readerDone:
		select {
		case message := <-p.events:
			return message, nil
		default:
		}
		return phaseBarrierMessage{}, fmt.Errorf("phase event stream closed: %s", p.failureDetail())
	case <-ctx.Done():
		return phaseBarrierMessage{}, fmt.Errorf("wait for phase event: %w", ctx.Err())
	}
}

// Release lets a child continue past the named barrier.
func (p *BarrierProcess) Release(point BarrierPoint) error {
	if !validBarrierPoint(point) {
		return fmt.Errorf("unknown phase barrier %q", point)
	}
	encoded, err := json.Marshal(phaseBarrierReply{Type: phaseBarrierContinue, Point: point})
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if _, err := p.control.Write(encoded); err != nil {
		return fmt.Errorf("release phase barrier %q: %w", point, err)
	}
	return nil
}

// KillAndWait sends SIGKILL to this exact child PID, reaps it, and confirms it
// exited by signal. It never searches for or signals a process by name.
func (p *BarrierProcess) KillAndWait() error {
	if p.Exited() {
		return errors.New("phase child exited before kill")
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill phase child PID %d: %w", p.cmd.Process.Pid, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), phaseBarrierWaitTimeout)
	defer cancel()
	if err := p.Wait(ctx); err == nil {
		return errors.New("phase child exited successfully instead of being killed")
	}
	if p.cmd.ProcessState == nil || p.cmd.ProcessState.ExitCode() != -1 {
		return fmt.Errorf("phase child PID %d did not exit by signal: %s", p.cmd.Process.Pid, p.failureDetail())
	}
	_ = p.control.Close()
	return nil
}

// Wait waits for the child to exit and returns its process result.
func (p *BarrierProcess) Wait(ctx context.Context) error {
	select {
	case <-p.processDone:
		p.processMu.Lock()
		defer p.processMu.Unlock()
		return p.processErr
	case <-ctx.Done():
		return fmt.Errorf("wait for phase child PID %d: %w", p.cmd.Process.Pid, ctx.Err())
	}
}

// Exited reports whether the exact child process has been reaped.
func (p *BarrierProcess) Exited() bool {
	select {
	case <-p.processDone:
		return true
	default:
		return false
	}
}

// Output returns bounded stdout/stderr captured for failure diagnostics when
// the command did not provide its own writers.
func (p *BarrierProcess) Output() string { return p.output.String() }

func (p *BarrierProcess) failureDetail() string {
	if output := p.Output(); output != "" {
		return output
	}
	p.readerMu.Lock()
	readerErr := p.readerErr
	p.readerMu.Unlock()
	if readerErr != nil {
		return readerErr.Error()
	}
	p.processMu.Lock()
	err := p.processErr
	p.processMu.Unlock()
	if err != nil {
		return err.Error()
	}
	return "no child output"
}

func (p *BarrierProcess) cleanup(t testing.TB) {
	if !p.Exited() {
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill leaked phase child PID %d: %v", p.cmd.Process.Pid, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), phaseBarrierWaitTimeout)
		defer cancel()
		if err := p.Wait(ctx); err != nil && !p.Exited() {
			t.Errorf("reap phase child PID %d: %v", p.cmd.Process.Pid, err)
		}
	}
	p.closeOnce.Do(func() {
		_ = p.control.Close()
		_ = p.reader.Close()
	})
}

type limitedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedOutput) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = b.buf.Write(value[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(value)
		}
	} else if len(value) > 0 {
		b.truncated = true
	}
	return len(value), nil
}

func (b *limitedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.buf.String()
	if b.truncated {
		result += "\n[phase process output truncated]"
	}
	return result
}
