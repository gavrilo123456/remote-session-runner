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
}

// PersistentShellResult is the result of one sourced script. The shell itself
// remains alive after a normal command, so state changes survive the next run.
type PersistentShellResult struct {
	CommandStarted  ControlFrame
	CommandComplete ControlFrame
	Stdout          []byte
	Stderr          []byte
	Chunks          []OutputChunk
}

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
	mu              sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	control         io.ReadCloser
	parser          *ControlParser
	workspace       string
	removeOnClose   bool
	boundaryTimeout time.Duration
	closed          bool
	lost            bool
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
	return &PersistentShell{cmd: cmd, stdin: stdinWriter, control: controlRead, parser: parser, workspace: workspace, removeOnClose: removeOnClose, boundaryTimeout: boundaryTimeout}, nil
}

// RunScript atomically materializes a private script, sources it in the
// existing Bash process, and waits for its dedicated control completion.
func (s *PersistentShell) RunScript(ctx context.Context, commandID string, script []byte) (PersistentShellResult, error) {
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
	go func() { stdoutDone <- drainOutputFIFO(ctx, stdoutRead, OutputStreamStdout, &sequence) }()
	go func() { stderrDone <- drainOutputFIFO(ctx, stderrRead, OutputStreamStderr, &sequence) }()

	started := ControlFrame{Version: ControlProtocolVersion, Type: FrameTypeCommandStarted, SessionID: s.parser.sessionID, CommandID: commandID, Generation: s.parser.generation}
	if err := started.Validate(); err != nil {
		return PersistentShellResult{}, err
	}
	startedWire, err := shellControlWrites(started)
	if err != nil {
		return PersistentShellResult{}, err
	}
	wrapper := startedWire + "\n" +
		"source " + shellQuote(scriptPath) + " >" + shellQuote(stdoutPath) + " 2>" + shellQuote(stderrPath) + "\n" +
		"runner_status=$?\n" +
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
	return PersistentShellResult{CommandStarted: gotStarted, CommandComplete: gotComplete, Stdout: stdout, Stderr: stderr, Chunks: chunks}, nil
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

func drainOutputFIFO(ctx context.Context, file *os.File, stream OutputStream, sequence *atomic.Uint64) outputDrainResult {
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
			pending = append(pending, buffer[:n]...)
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

func chunksBytes(chunks []OutputChunk, stream OutputStream) []byte {
	var result []byte
	for _, chunk := range chunks {
		if chunk.Stream == stream {
			result = append(result, chunk.Data...)
		}
	}
	return result
}
