// Package sshclient provides the Mac-side transport for the restricted SSH
// bridge. It owns only SSH process setup and bounded NDJSON exchange; remote
// command results remain bridge replies and are never converted to transport
// failures.
package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
)

const fixedBridgeCommand = "runner-ssh-bridge --stdio"

var (
	ErrConfiguration      = errors.New("SSH client configuration is invalid")
	ErrTransport          = errors.New("SSH bridge transport failed")
	ErrReplyRequestID     = errors.New("SSH bridge reply request_id does not match")
	ErrUnexpectedReply    = errors.New("SSH bridge returned more than one reply")
	ErrNoReply            = errors.New("SSH bridge closed without a reply")
	ErrStreamTerminated   = errors.New("SSH bridge stream ended without a terminal frame")
	ErrFrameLimit         = domain.ErrSerializedInputTooLarge
	ErrUnsupportedCommand = errors.New("SSH bridge command is not the fixed command")
)

// TransportPhase distinguishes a definite non-delivery from an uncertain
// mutation after request bytes reached the SSH process.
type TransportPhase string

const (
	PhaseBeforeSend TransportPhase = "before_send"
	PhaseAfterSend  TransportPhase = "after_send"
)

// TransportError is returned only for local/SSH/channel failures. A remote
// bridge error reply is returned as a ReplyFrame with no TransportError.
type TransportError struct {
	Phase TransportPhase
	Err   error
}

func (e *TransportError) Error() string {
	if e == nil {
		return ErrTransport.Error()
	}
	return fmt.Sprintf("%s (%s): %v", ErrTransport, e.Phase, e.Err)
}

// Is lets callers test both the transport sentinel and the original cause.
func (e *TransportError) Is(target error) bool {
	if target == ErrTransport {
		return true
	}
	return e != nil && errors.Is(e.Err, target)
}

func (e *TransportError) Unwrap() error { return e.Err }

// Config defines one pinned SSH bridge destination. IdentityFile and
// KnownHostsFile must be absolute owner-controlled paths. SSHConfig is not
// consulted: the client uses -F /dev/null and supplies every security option.
type Config struct {
	SSHPath        string
	User           string
	Host           string
	Port           int
	IdentityFile   string
	KnownHostsFile string
	BridgeCommand  string
	ConnectTimeout time.Duration
	MaxFrameBytes  int
}

// Client is safe for concurrent use. Each call uses a fresh SSH process so a
// reconnect never silently reuses a possibly-corrupted stream. Callers retry
// the exact same RequestFrame to preserve its request/resource/idempotency IDs.
type Client struct {
	config Config
}

// New validates a pinned bridge configuration.
func New(config Config) (*Client, error) {
	if config.SSHPath == "" {
		config.SSHPath = "ssh"
	}
	if strings.TrimSpace(config.User) == "" || strings.ContainsAny(config.User, " \t\r\n") {
		return nil, fmt.Errorf("%w: user must be a single non-empty token", ErrConfiguration)
	}
	if strings.TrimSpace(config.Host) == "" || strings.ContainsAny(config.Host, " \t\r\n") {
		return nil, fmt.Errorf("%w: host must be a single non-empty token", ErrConfiguration)
	}
	if config.Port == 0 {
		config.Port = 22
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, fmt.Errorf("%w: port must be between 1 and 65535", ErrConfiguration)
	}
	if err := validateAbsolutePath(config.IdentityFile, "identity file"); err != nil {
		return nil, err
	}
	if err := validateAbsolutePath(config.KnownHostsFile, "known-hosts file"); err != nil {
		return nil, err
	}
	if config.BridgeCommand == "" {
		config.BridgeCommand = fixedBridgeCommand
	}
	if config.BridgeCommand != fixedBridgeCommand {
		return nil, fmt.Errorf("%w: got %q", ErrUnsupportedCommand, config.BridgeCommand)
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 15 * time.Second
	}
	if config.MaxFrameBytes == 0 {
		config.MaxFrameBytes = domain.MaxSerializedFrameBytes
	}
	if config.MaxFrameBytes < 1 || config.MaxFrameBytes > domain.MaxSerializedFrameBytes {
		return nil, fmt.Errorf("%w: frame limit must be between 1 and %d bytes", ErrConfiguration, domain.MaxSerializedFrameBytes)
	}
	return &Client{config: config}, nil
}

func validateAbsolutePath(value, label string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%w: %s must be an absolute clean path", ErrConfiguration, label)
	}
	return nil
}

// Config returns a copy of the validated configuration.
func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	return c.config
}

// CommandArguments exposes the exact non-secret argument vector for tests and
// deployment diagnostics. The returned slice is independent of the client.
func (c *Client) CommandArguments() []string {
	if c == nil {
		return nil
	}
	args := c.commandArguments()
	return append([]string(nil), args...)
}

func (c *Client) commandArguments() []string {
	seconds := int(c.config.ConnectTimeout / time.Second)
	if c.config.ConnectTimeout%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return []string{
		"-F", "/dev/null",
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "RequestTTY=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", openSSHPathOption("UserKnownHostsFile", c.config.KnownHostsFile),
		"-p", strconv.Itoa(c.config.Port),
		"-i", c.config.IdentityFile,
		"-l", c.config.User,
		c.config.Host,
		"runner-ssh-bridge", "--stdio",
	}
}

// OpenSSH config values are parsed a second time by ssh. Quoting the path
// inside the -o argument preserves spaces in the Mac service-root path.
func openSSHPathOption(name, path string) string {
	path = strings.ReplaceAll(path, `\`, `\\`)
	path = strings.ReplaceAll(path, `"`, `\"`)
	return name + `="` + path + `"`
}

// Call performs one request/reply exchange. The request IDs and payload are
// sent exactly as supplied; retrying Call with the same frame is the stable
// mutation retry mechanism.
func (c *Client) Call(ctx context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	var replies []sshbridge.ReplyFrame
	err := c.exchange(ctx, request, false, func(reply sshbridge.ReplyFrame) error {
		replies = append(replies, reply)
		return nil
	})
	if err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	if len(replies) == 0 {
		return sshbridge.ReplyFrame{}, &TransportError{Phase: PhaseAfterSend, Err: ErrNoReply}
	}
	if len(replies) != 1 {
		return sshbridge.ReplyFrame{}, fmt.Errorf("%w: got %d", ErrUnexpectedReply, len(replies))
	}
	return replies[0], nil
}

// RoundTrip is an alias for Call for transport-oriented callers.
func (c *Client) RoundTrip(ctx context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	return c.Call(ctx, request)
}

// Stream performs a replay/follow request and invokes receive in wire order.
// It returns the bridge error reply as data; only transport/protocol failures
// are returned as Go errors.
func (c *Client) Stream(ctx context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if receive == nil {
		return fmt.Errorf("%w: nil stream receiver", ErrConfiguration)
	}
	return c.exchange(ctx, request, true, receive)
}

func (c *Client) exchange(ctx context.Context, request sshbridge.RequestFrame, stream bool, receive func(sshbridge.ReplyFrame) error) error {
	if c == nil {
		return &TransportError{Phase: PhaseBeforeSend, Err: ErrConfiguration}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frame, err := encodeBoundedRequest(request, c.config.MaxFrameBytes)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.config.SSHPath, c.commandArguments()...)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &TransportError{Phase: PhaseBeforeSend, Err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return &TransportError{Phase: PhaseBeforeSend, Err: err}
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return &TransportError{Phase: PhaseBeforeSend, Err: err}
	}

	sent := false
	finish := func(phase TransportPhase, cause error) error {
		if cause == nil {
			return nil
		}
		return &TransportError{Phase: phase, Err: cause}
	}
	n, writeErr := stdin.Write(frame)
	if n > 0 {
		sent = true
	}
	if writeErr != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if sent {
			return finish(PhaseAfterSend, writeErr)
		}
		return finish(PhaseBeforeSend, writeErr)
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return finish(PhaseAfterSend, err)
	}
	sent = true

	decoder := sshbridge.NewDecoderWithLimit(stdout, c.config.MaxFrameBytes)
	replyCount := 0
	sawStreamEnd := false
	sawBridgeError := false
	for {
		reply, decodeErr := decoder.DecodeReply()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if decodeErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return finish(PhaseAfterSend, decodeErr)
		}
		if reply.RequestID != request.RequestID {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("%w: got %q, want %q", ErrReplyRequestID, reply.RequestID, request.RequestID)
		}
		replyCount++
		if reply.ResponseType == "stream_end" {
			sawStreamEnd = true
		}
		if reply.ResponseType == "error" {
			sawBridgeError = true
		}
		if err := receive(reply); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
		if !stream {
			// A non-stream call still drains to EOF so the SSH exit status is
			// checked and a post-send channel failure is not hidden.
			continue
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return finish(PhaseAfterSend, waitErr)
	}
	if replyCount == 0 {
		return finish(PhaseAfterSend, ErrNoReply)
	}
	if stream && !sawStreamEnd && !sawBridgeError {
		return finish(PhaseAfterSend, ErrStreamTerminated)
	}
	_ = sent
	return nil
}

func encodeBoundedRequest(request sshbridge.RequestFrame, maxBytes int) ([]byte, error) {
	var buffer bytes.Buffer
	if err := sshbridge.EncodeRequest(&buffer, request); err != nil {
		return nil, err
	}
	frame := buffer.Bytes()
	if len(frame) == 0 || frame[len(frame)-1] != '\n' {
		return nil, fmt.Errorf("%w: request framing failed", ErrFrameLimit)
	}
	if len(frame)-1 > maxBytes {
		return nil, fmt.Errorf("%w: frame is %d bytes, maximum %d", ErrFrameLimit, len(frame)-1, maxBytes)
	}
	return append([]byte(nil), frame...), nil
}

// FileExists is a small deployment check used by host setup and tests. It is
// deliberately separate from New so fake command paths can be injected.
func FileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
