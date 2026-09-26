package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	ErrPersistentShellClosed  = errors.New("persistent shell is closed")
	ErrPersistentShellCommand = errors.New("persistent shell command failed")
	ErrPersistentShellExited  = errors.New("persistent shell exited")
	ErrPersistentShellLost    = errors.New("persistent shell is lost")
	ErrOutputBoundary         = errors.New("command output boundary is unconfirmed")
	ErrNoActiveCommand        = errors.New("no active persistent-shell command")
)

// PersistentShellOptions controls the real Bash process used for one session.
// Workspace is private to the session when omitted; a supplied workspace is
// used as-is and must already be owned by the caller's account.
type PersistentShellOptions struct {
	SessionID             string
	Generation            string
	ShellPath             string
	Workspace             string
	Descriptors           ReservedDescriptors
	OutputBoundaryTimeout time.Duration
	MaxOutputBytes        int64
	ProcessInspector      ProcessInspector
	DescendantKiller      DescendantKiller
}

// PersistentShellResult is the result of one sourced script. The shell itself
// remains alive after a normal command, so state changes survive the next run.
type PersistentShellResult struct {
	CommandStarted         ControlFrame
	CommandComplete        ControlFrame
	Stdout                 []byte
	Stderr                 []byte
	Chunks                 []OutputChunk
	OutputTruncated        bool
	OutputTruncationEvents int
}

// PersistentShellStopResult reports whether an interrupt reached a clean
// command boundary. A false result means the shell must be treated as lost.
type PersistentShellStopResult struct {
	CommandID string
	Confirmed bool
}

// DescendantProcess is a best-effort process-tree observation under the
// account running the session. It is lifecycle evidence, not confinement.
type DescendantProcess struct {
	PID     int
	Parent  int
	Command string
}

// DescendantCleanupResult records whether all observed descendants stopped.
type DescendantCleanupResult struct {
	Confirmed bool
	Remaining []DescendantProcess
}

// ProcessInspector and DescendantKiller are injectable host seams for
// hermetic lifecycle tests. Defaults use the host process table and signals.
type ProcessInspector func(rootPID int) ([]DescendantProcess, error)
type DescendantKiller func(rootPID int, signal syscall.Signal) int

const (
	// MaxOutputChunkBytes is the raw-byte ceiling for one agent output chunk.
	MaxOutputChunkBytes = 16 * 1024
	// OutputFlushInterval is the normal-load upper bound for a partial chunk.
	OutputFlushInterval = 50 * time.Millisecond
)

// OutputStream identifies the command-scoped raw byte pipe.
type OutputStream string

const (
	OutputStreamStdout OutputStream = "stdout"
	OutputStreamStderr OutputStream = "stderr"
)

// OutputChunk is a raw byte chunk drained from one command pipe. Data is never
// converted through a text encoding or merged with the other stream.
type OutputChunk struct {
	Sequence uint64
	Stream   OutputStream
	Data     []byte
}

// PersistentBash is the agent-facing name for the same one-process runtime.
// The aliases keep the shell contract explicit without introducing a second
// implementation or a replacement-shell path.
type PersistentBash = PersistentShell
type PersistentBashOptions = PersistentShellOptions
type PersistentBashResult = PersistentShellResult

// NewPersistentShell is a constructor spelling convenient for adapters.
func NewPersistentShell(ctx context.Context, options PersistentShellOptions) (*PersistentShell, error) {
	return StartPersistentShell(ctx, options)
}

// NewPersistentBash is the agent-facing constructor spelling.
func NewPersistentBash(ctx context.Context, options PersistentBashOptions) (*PersistentBash, error) {
	return StartPersistentShell(ctx, options)
}

// PersistentShell owns one long-lived Bash process. Calls to RunScript are
// serialized so a script and its control frame cannot overlap another command.
type PersistentShell struct {
	mu                 sync.Mutex
	stateMu            sync.Mutex
	cmd                *exec.Cmd
	stdin              io.WriteCloser
	control            io.ReadCloser
	parser             *ControlParser
	workspace          string
	removeOnClose      bool
	boundaryTimeout    time.Duration
	maxOutputBytes     int64
	closed             bool
	lost               bool
	activeCommand      string
	activeDone         chan struct{}
	cancelRequested    bool
	stopConfirmed      bool
	cleanupConfirmed   bool
	inspectDescendants ProcessInspector
	killDescendants    DescendantKiller
}

// StartPersistentShell starts one Bash process with a dedicated control-write
// descriptor. It deliberately does not create a replacement shell after exit.
func StartPersistentShell(ctx context.Context, options PersistentShellOptions) (*PersistentShell, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrPersistentShellCommand)
	}
	parser, err := NewControlParser(options.SessionID, options.Generation)
	if err != nil {
		return nil, err
	}
	descriptors := options.Descriptors
	if descriptors == (ReservedDescriptors{}) {
		descriptors = DefaultReservedDescriptors()
	}
	if err := descriptors.Validate(); err != nil {
		return nil, err
	}
	shellPath := options.ShellPath
	if shellPath == "" {
		shellPath = "/bin/bash"
	}
	workspace := options.Workspace
	removeOnClose := false
	if workspace == "" {
		workspace, err = os.MkdirTemp("", "remote-session-runner-")
		if err != nil {
			return nil, fmt.Errorf("%w: create workspace: %v", ErrPersistentShellCommand, err)
		}
		removeOnClose = true
	} else if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create workspace: %v", ErrPersistentShellCommand, err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		if removeOnClose {
			_ = os.RemoveAll(workspace)
		}
		return nil, fmt.Errorf("%w: protect workspace: %v", ErrPersistentShellCommand, err)
	}
	boundaryTimeout := options.OutputBoundaryTimeout
	if boundaryTimeout <= 0 {
		boundaryTimeout = time.Second
	}
	maxOutputBytes := options.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = 100 * 1024 * 1024
	}

	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		if removeOnClose {
			_ = os.RemoveAll(workspace)
		}
		return nil, fmt.Errorf("%w: control pipe: %v", ErrPersistentShellCommand, err)
	}
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		_ = controlRead.Close()
		_ = controlWrite.Close()
		if removeOnClose {
			_ = os.RemoveAll(workspace)
		}
		return nil, fmt.Errorf("%w: stdin pipe: %v", ErrPersistentShellCommand, err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = controlRead.Close()
		_ = controlWrite.Close()
		if removeOnClose {
			_ = os.RemoveAll(workspace)
		}
		return nil, fmt.Errorf("%w: reserved read descriptor: %v", ErrPersistentShellCommand, err)
	}

	cmd := exec.CommandContext(ctx, shellPath, "--noprofile", "--norc", "-s")
	cmd.Dir = workspace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = stdinReader
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.ExtraFiles = []*os.File{devNull, controlWrite}
	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = controlRead.Close()
		_ = controlWrite.Close()
		if removeOnClose {
			_ = os.RemoveAll(workspace)
		}
		return nil, fmt.Errorf("%w: start bash: %v", ErrPersistentShellCommand, err)
	}
	// ExtraFiles are inherited by the child; the parent retains only the read
	// side needed for completion frames.
	_ = devNull.Close()
	_ = controlWrite.Close()
	_ = stdinReader.Close()
	inspector := options.ProcessInspector
	if inspector == nil {
		inspector = inspectProcessDescendants
	}
	killer := options.DescendantKiller
	if killer == nil {
		killer = signalDescendants
	}
	return &PersistentShell{cmd: cmd, stdin: stdinWriter, control: controlRead, parser: parser, workspace: workspace, removeOnClose: removeOnClose, boundaryTimeout: boundaryTimeout, maxOutputBytes: maxOutputBytes, inspectDescendants: inspector, killDescendants: killer}, nil
}

// RunScript atomically materializes a private script, sources it in the
// existing Bash process, and waits for its dedicated control completion.
func (s *PersistentShell) RunScript(ctx context.Context, commandID string, script []byte) (result PersistentShellResult, runErr error) {
	if s == nil {
		return PersistentShellResult{}, ErrPersistentShellClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return PersistentShellResult{}, ErrPersistentShellClosed
	}
	if s.lost {
		return PersistentShellResult{}, ErrPersistentShellLost
	}
	if ctx == nil {
		return PersistentShellResult{}, fmt.Errorf("%w: nil context", ErrPersistentShellCommand)
	}
	if err := validateControlValue("command_id", commandID, 256); err != nil {
		return PersistentShellResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PersistentShellResult{}, err
	}
	s.stateMu.Lock()
	s.activeCommand = commandID
	s.activeDone = make(chan struct{})
	s.cancelRequested = false
	s.stopConfirmed = false
	activeDone := s.activeDone
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		if s.cancelRequested && runErr == nil && !s.lost && s.processAlive() {
			s.stopConfirmed = true
		}
		close(activeDone)
		s.activeCommand = ""
		s.activeDone = nil
		s.stateMu.Unlock()
	}()

	scriptPath, err := writePrivateSynced(s.workspace, "script-*.sh", script)
	if err != nil {
		return PersistentShellResult{}, err
	}
	stdoutPath, stdoutRead, stdoutKeepalive, err := createOutputFIFO(s.workspace, "stdout-*.fifo")
	if err != nil {
		_ = os.Remove(scriptPath)
		return PersistentShellResult{}, err
	}
	stderrPath, stderrRead, stderrKeepalive, err := createOutputFIFO(s.workspace, "stderr-*.fifo")
	if err != nil {
		_ = os.Remove(scriptPath)
		_ = stdoutRead.Close()
		_ = stdoutKeepalive.Close()
		_ = os.Remove(stdoutPath)
		return PersistentShellResult{}, err
	}
	defer func() {
		_ = os.Remove(scriptPath)
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		_ = stdoutRead.Close()
		_ = stderrRead.Close()
		_ = stdoutKeepalive.Close()
		_ = stderrKeepalive.Close()
	}()
	stdoutDone := make(chan outputDrainResult, 1)
	stderrDone := make(chan outputDrainResult, 1)
	var sequence atomic.Uint64
	limiter := &outputLimiter{max: s.maxOutputBytes}
	go func() { stdoutDone <- drainOutputFIFO(ctx, stdoutRead, OutputStreamStdout, &sequence, limiter) }()
	go func() { stderrDone <- drainOutputFIFO(ctx, stderrRead, OutputStreamStderr, &sequence, limiter) }()

	started := ControlFrame{Version: ControlProtocolVersion, Type: FrameTypeCommandStarted, SessionID: s.parser.sessionID, CommandID: commandID, Generation: s.parser.generation}
	if err := started.Validate(); err != nil {
		return PersistentShellResult{}, err
	}
	startedWire, err := shellControlWrites(started)
	if err != nil {
		return PersistentShellResult{}, err
	}
	wrapper := startedWire + "\n" +
		"runner_interrupt=0\n" +
		"trap 'runner_interrupt=1' INT\n" +
		"source " + shellQuote(scriptPath) + " >" + shellQuote(stdoutPath) + " 2>" + shellQuote(stderrPath) + "\n" +
		"runner_status=$?\n" +
		"trap - INT\n" +
		"\n" +
		"if [ \"$runner_status\" -gt 127 ]; then runner_status=$((runner_status-256)); fi\n" +
		"case \"$runner_status\" in -*) ;; *) ;; esac\n"
	// The completion frame is emitted by a small fixed shell helper below. The
	// exit code is substituted only after the source returns; script bytes are
	// never interpolated into this wrapper or a control frame.
	wrapper += "runner_frame_status=\"$runner_status\"\n"
	// The body/header are selected by the helper's exit-code case. This keeps
	// the shell syntax fixed while allowing all normal Bash status values.
	for code := -128; code <= 127; code++ {
		frame := ControlFrame{Version: ControlProtocolVersion, Type: FrameTypeCommandComplete, SessionID: s.parser.sessionID, CommandID: commandID, Generation: s.parser.generation, ExitCode: &code}
		wire, err := shellControlWrites(frame)
		if err != nil {
			return PersistentShellResult{}, err
		}
		wrapper += fmt.Sprintf("if [ \"$runner_frame_status\" = %d ]; then\n%s\nfi\n", code, wire)
	}
	// Bash returns statuses in [0,255]. The case list above covers 0..127 and
	// the negative signal-style values produced by a killed command; statuses
	// 128..255 map to -128..-1 in the helper's normalization.
	wrapperPath, err := writePrivateSynced(s.workspace, "wrapper-*.sh", []byte(wrapper))
	if err != nil {
		return PersistentShellResult{}, err
	}
	defer os.Remove(wrapperPath)

	if _, err := io.WriteString(s.stdin, "source "+shellQuote(wrapperPath)+"\n"); err != nil {
		s.lost = true
		return PersistentShellResult{}, fmt.Errorf("%w: write command: %v", ErrPersistentShellCommand, err)
	}
	gotStarted, err := s.parser.Read(s.control)
	if err != nil {
		s.lost = true
		return PersistentShellResult{}, fmt.Errorf("%w: read start: %v: %w", ErrPersistentShellExited, err, ErrPersistentShellCommand)
	}
	if gotStarted.Type != FrameTypeCommandStarted || gotStarted.CommandID != commandID {
		s.lost = true
		return PersistentShellResult{}, fmt.Errorf("%w: unexpected start frame %+v", ErrPersistentShellCommand, gotStarted)
	}
	gotComplete, err := s.parser.Read(s.control)
	if err != nil {
		s.lost = true
		return PersistentShellResult{}, fmt.Errorf("%w: read completion: %v: %w", ErrPersistentShellExited, err, ErrPersistentShellCommand)
	}
	if gotComplete.Type != FrameTypeCommandComplete || gotComplete.CommandID != commandID || gotComplete.ExitCode == nil {
		s.lost = true
		return PersistentShellResult{}, fmt.Errorf("%w: unexpected completion frame %+v", ErrPersistentShellCommand, gotComplete)
	}
	// The shell closes its command-scoped FIFO writers when source returns. The
	// parent keepalive writers are then closed to make EOF observable to both
	// drainers, establishing the P032 output boundary for this precursor.
	_ = stdoutKeepalive.Close()
	_ = stderrKeepalive.Close()
	stdoutResult, stderrResult, drainErr := waitForOutputBoundary(stdoutDone, stderrDone, s.boundaryTimeout, stdoutRead, stderrRead)
	if drainErr != nil {
		s.lost = true
		return PersistentShellResult{}, drainErr
	}
	if stdoutResult.err != nil {
		s.lost = true
		return PersistentShellResult{}, stdoutResult.err
	}
	if stderrResult.err != nil {
		s.lost = true
		return PersistentShellResult{}, stderrResult.err
	}
	chunks := append(stdoutResult.chunks, stderrResult.chunks...)
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].Sequence < chunks[j].Sequence })
	stdout := chunksBytes(chunks, OutputStreamStdout)
	stderr := chunksBytes(chunks, OutputStreamStderr)
	return PersistentShellResult{CommandStarted: gotStarted, CommandComplete: gotComplete, Stdout: stdout, Stderr: stderr, Chunks: chunks, OutputTruncated: limiter.wasTruncated(), OutputTruncationEvents: limiter.truncationEvents()}, nil
}

// CancelCurrentCommand requests a graceful SIGINT for the active command and
// waits for its normal control/EOF barrier. If that boundary is not proven by
// the grace period, SIGKILL is sent to Bash and the result is unconfirmed.
func (s *PersistentShell) CancelCurrentCommand(ctx context.Context, grace time.Duration) (PersistentShellStopResult, error) {
	if s == nil {
		return PersistentShellStopResult{}, ErrPersistentShellClosed
	}
	if ctx == nil {
		return PersistentShellStopResult{}, fmt.Errorf("%w: nil context", ErrPersistentShellCommand)
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	s.stateMu.Lock()
	commandID := s.activeCommand
	done := s.activeDone
	if commandID == "" || done == nil {
		s.stateMu.Unlock()
		return PersistentShellStopResult{}, ErrNoActiveCommand
	}
	s.cancelRequested = true
	process := s.cmd.Process
	s.stateMu.Unlock()
	if process == nil {
		return PersistentShellStopResult{CommandID: commandID}, ErrPersistentShellLost
	}
	if err := signalProcessGroup(process.Pid, syscall.SIGINT); err != nil {
		if count := signalDescendants(process.Pid, syscall.SIGINT); count == 0 {
			return PersistentShellStopResult{CommandID: commandID}, fmt.Errorf("%w: signal interrupt: %v", ErrPersistentShellCommand, err)
		}
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		s.stateMu.Lock()
		confirmed := s.stopConfirmed
		s.stateMu.Unlock()
		if confirmed && !s.processAlive() {
			s.mu.Lock()
			s.lost = true
			s.mu.Unlock()
			return PersistentShellStopResult{CommandID: commandID}, ErrPersistentShellLost
		}
		return PersistentShellStopResult{CommandID: commandID, Confirmed: confirmed}, nil
	case <-ctx.Done():
		return PersistentShellStopResult{CommandID: commandID}, ctx.Err()
	case <-timer.C:
		_ = signalProcessGroup(process.Pid, syscall.SIGKILL)
		settle := time.NewTimer(100 * time.Millisecond)
		defer settle.Stop()
		select {
		case <-done:
			s.stateMu.Lock()
			confirmed := s.stopConfirmed
			s.stateMu.Unlock()
			if confirmed && s.processAlive() {
				return PersistentShellStopResult{CommandID: commandID, Confirmed: true}, nil
			}
			return PersistentShellStopResult{CommandID: commandID, Confirmed: false}, ErrPersistentShellLost
		case <-settle.C:
			return PersistentShellStopResult{CommandID: commandID, Confirmed: false}, ErrPersistentShellLost
		}
	}
}

func (s *PersistentShell) processAlive() bool {
	if s == nil || s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return false
	}
	return s.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func signalDescendants(rootPID int, signal syscall.Signal) int {
	descendants, err := inspectProcessDescendants(rootPID)
	if err != nil {
		return 0
	}
	count := 0
	for _, descendant := range descendants {
		if syscall.Kill(descendant.PID, signal) == nil {
			count++
		}
	}
	return count
}

func inspectProcessDescendants(rootPID int) ([]DescendantProcess, error) {
	output, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect descendants: %v", ErrPersistentShellCommand, err)
	}
	type process struct {
		pid, parent int
		command     string
	}
	children := make(map[int][]process)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr == nil && parentErr == nil {
			children[parent] = append(children[parent], process{pid: pid, parent: parent, command: strings.Join(fields[2:], " ")})
		}
	}
	queue := append([]process(nil), children[rootPID]...)
	result := make([]DescendantProcess, 0, len(queue))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, DescendantProcess{PID: current.pid, Parent: current.parent, Command: current.command})
		queue = append(queue, children[current.pid]...)
	}
	return result, nil
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err == nil {
		return nil
	}
	return syscall.Kill(pid, signal)
}

// StopCommand is an adapter-friendly alias for CancelCurrentCommand.
func (s *PersistentShell) StopCommand(ctx context.Context, grace time.Duration) (PersistentShellStopResult, error) {
	return s.CancelCurrentCommand(ctx, grace)
}

// InspectDescendants returns the current descendants of the persistent Bash.
func (s *PersistentShell) InspectDescendants() ([]DescendantProcess, error) {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil, ErrPersistentShellClosed
	}
	return s.inspectDescendants(s.cmd.Process.Pid)
}

// CleanupDescendants sends bounded TERM/KILL signals to observed descendants.
// A false result retains capacity until a later reconciliation pass.
func (s *PersistentShell) CleanupDescendants(ctx context.Context, grace time.Duration) (DescendantCleanupResult, error) {
	if s == nil {
		return DescendantCleanupResult{}, ErrPersistentShellClosed
	}
	if ctx == nil {
		return DescendantCleanupResult{}, fmt.Errorf("%w: nil context", ErrPersistentShellCommand)
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	if descendants, err := s.InspectDescendants(); err != nil {
		return DescendantCleanupResult{}, err
	} else if len(descendants) == 0 {
		s.stateMu.Lock()
		s.cleanupConfirmed = true
		s.stateMu.Unlock()
		return DescendantCleanupResult{Confirmed: true}, nil
	}
	_ = s.killDescendants(s.cmd.Process.Pid, syscall.SIGTERM)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	for {
		remaining, err := s.InspectDescendants()
		if err != nil {
			return DescendantCleanupResult{}, err
		}
		if len(remaining) == 0 {
			s.stateMu.Lock()
			s.cleanupConfirmed = true
			s.stateMu.Unlock()
			return DescendantCleanupResult{Confirmed: true}, nil
		}
		select {
		case <-ctx.Done():
			return DescendantCleanupResult{Remaining: remaining}, ctx.Err()
		case <-deadline.C:
			_ = s.killDescendants(s.cmd.Process.Pid, syscall.SIGKILL)
			remaining, _ = s.InspectDescendants()
			confirmed := len(remaining) == 0
			s.stateMu.Lock()
			s.cleanupConfirmed = confirmed
			s.stateMu.Unlock()
			return DescendantCleanupResult{Confirmed: confirmed, Remaining: remaining}, nil
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// CapacityRetained reports whether uncertain runtime resources remain
// reserved until cleanup/reconciliation proves the descendants are gone.
func (s *PersistentShell) CapacityRetained() bool {
	if s == nil {
		return false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.lost && !s.cleanupConfirmed
}

func waitForOutputBoundary(stdoutDone, stderrDone <-chan outputDrainResult, timeout time.Duration, stdoutRead, stderrRead *os.File) (outputDrainResult, outputDrainResult, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var stdoutResult, stderrResult outputDrainResult
	stdoutReady, stderrReady := false, false
	for !stdoutReady || !stderrReady {
		select {
		case stdoutResult = <-stdoutDone:
			stdoutReady = true
		case stderrResult = <-stderrDone:
			stderrReady = true
		case <-timer.C:
			_ = stdoutRead.Close()
			_ = stderrRead.Close()
			return outputDrainResult{}, outputDrainResult{}, fmt.Errorf("%w: timed out after %s", ErrOutputBoundary, timeout)
		}
	}
	return stdoutResult, stderrResult, nil
}

// Close closes the command channel and waits for the one Bash process. It
// never starts a replacement shell and is idempotent.
func (s *PersistentShell) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.stdin.Close()
	_ = s.control.Close()
	waitErr := s.cmd.Wait()
	if s.removeOnClose {
		if err := os.RemoveAll(s.workspace); waitErr == nil {
			waitErr = err
		}
	}
	return waitErr
}

func shellControlWrites(frame ControlFrame) (string, error) {
	body, err := json.Marshal(frame)
	if err != nil {
		return "", err
	}
	if uint32(len(body)) > MaxControlFrameBytes {
		return "", ErrControlFrameTooLarge
	}
	var escaped strings.Builder
	escaped.Grow(len(body) * 4)
	escaped.WriteString("printf '%b' '")
	var header [4]byte
	header[0] = byte(len(body) >> 24)
	header[1] = byte(len(body) >> 16)
	header[2] = byte(len(body) >> 8)
	header[3] = byte(len(body))
	for _, b := range header {
		fmt.Fprintf(&escaped, "\\x%02x", b)
	}
	escaped.WriteString("' >&4\n")
	escaped.WriteString("printf '%s' ")
	escaped.WriteString(shellQuote(string(body)))
	escaped.WriteString(" >&4")
	return escaped.String(), nil
}

func writePrivateSynced(dir, pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("%w: create private file: %v", ErrPersistentShellCommand, err)
	}
	path := file.Name()
	if err := writePrivateFileHandle(file, data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writePrivateFileHandle(file *os.File, data []byte) error {
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("%w: chmod private file: %v", ErrPersistentShellCommand, err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("%w: write private file: %v", ErrPersistentShellCommand, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync private file: %v", ErrPersistentShellCommand, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close private file: %v", ErrPersistentShellCommand, err)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type outputDrainResult struct {
	chunks []OutputChunk
	err    error
}

func createOutputFIFO(dir, pattern string) (string, *os.File, *os.File, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: create output fifo: %v", ErrPersistentShellCommand, err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, nil, fmt.Errorf("%w: close output fifo placeholder: %v", ErrPersistentShellCommand, err)
	}
	if err := os.Remove(path); err != nil {
		return "", nil, nil, fmt.Errorf("%w: remove output fifo placeholder: %v", ErrPersistentShellCommand, err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return "", nil, nil, fmt.Errorf("%w: create output fifo: %v", ErrPersistentShellCommand, err)
	}
	read, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		_ = os.Remove(path)
		return "", nil, nil, fmt.Errorf("%w: open output fifo: %v", ErrPersistentShellCommand, err)
	}
	keepalive, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		_ = read.Close()
		_ = os.Remove(path)
		return "", nil, nil, fmt.Errorf("%w: keep output fifo open: %v", ErrPersistentShellCommand, err)
	}
	return path, read, keepalive, nil
}

func drainOutputFIFO(ctx context.Context, file *os.File, stream OutputStream, sequence *atomic.Uint64, limiter *outputLimiter) outputDrainResult {
	var result outputDrainResult
	buffer := make([]byte, MaxOutputChunkBytes)
	pending := make([]byte, 0, MaxOutputChunkBytes)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		data := append([]byte(nil), pending...)
		result.chunks = append(result.chunks, OutputChunk{Sequence: sequence.Add(1), Stream: stream, Data: data})
		pending = pending[:0]
	}
	ticker := time.NewTicker(OutputFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result.err = ctx.Err()
			return result
		case <-ticker.C:
			flush()
		default:
		}
		n, err := file.Read(buffer)
		if n > 0 {
			pending = append(pending, limiter.retain(buffer[:n])...)
			for len(pending) >= MaxOutputChunkBytes {
				data := append([]byte(nil), pending[:MaxOutputChunkBytes]...)
				result.chunks = append(result.chunks, OutputChunk{Sequence: sequence.Add(1), Stream: stream, Data: data})
				pending = pending[MaxOutputChunkBytes:]
			}
		}
		if err == io.EOF {
			flush()
			return result
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			result.err = fmt.Errorf("%w: drain %s: %v", ErrPersistentShellCommand, stream, err)
			return result
		}
		if n == 0 || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			time.Sleep(time.Millisecond)
		}
	}
}

type outputLimiter struct {
	mu              sync.Mutex
	max             int64
	retained        int64
	truncated       bool
	truncationCount int
}

func (l *outputLimiter) retain(data []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := l.max - l.retained
	if remaining <= 0 {
		l.markTruncated()
		return nil
	}
	if int64(len(data)) > remaining {
		allowed := append([]byte(nil), data[:remaining]...)
		l.retained += remaining
		l.markTruncated()
		return allowed
	}
	l.retained += int64(len(data))
	return data
}

func (l *outputLimiter) markTruncated() {
	if !l.truncated {
		l.truncated = true
		l.truncationCount = 1
	}
}

func (l *outputLimiter) wasTruncated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.truncated
}

func (l *outputLimiter) truncationEvents() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.truncationCount
}

func chunksBytes(chunks []OutputChunk, stream OutputStream) []byte {
	var result []byte
	for _, chunk := range chunks {
		if chunk.Stream == stream {
			result = append(result, chunk.Data...)
		}
	}
	return result
}
