package runtime

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// ControlProtocolVersion is the only control-frame version accepted by the
	// P030 agent seam.
	ControlProtocolVersion uint16 = 1
	// MaxControlFrameBytes bounds the encoded JSON body before allocation.
	MaxControlFrameBytes uint32 = 64 * 1024
	// ReservedControlReadFD and ReservedControlWriteFD are outside the normal
	// stdin/stdout/stderr range and are never part of the user script contract.
	ReservedControlReadFD  = 3
	ReservedControlWriteFD = 4
)

var (
	ErrControlProtocol       = errors.New("invalid agent control protocol")
	ErrControlFrameTooLarge  = errors.New("agent control frame is too large")
	ErrControlFrameMalformed = errors.New("malformed agent control frame")
	ErrControlGeneration     = errors.New("agent control generation mismatch")
	ErrControlSession        = errors.New("agent control session mismatch")
	ErrControlHandshake      = errors.New("invalid agent handshake")
	ErrReservedDescriptor    = errors.New("reserved descriptor layout is invalid")
)

// FrameType is the bounded control vocabulary between an executor and one
// session agent. User stdout/stderr are never parsed as these frames.
type FrameType string

const (
	FrameTypeHello           FrameType = "hello"
	FrameTypeReady           FrameType = "ready"
	FrameTypeCommandStarted  FrameType = "command_started"
	FrameTypeCommandComplete FrameType = "command_complete"
	FrameTypeCommandLost     FrameType = "command_lost"
	FrameTypeStopAck         FrameType = "stop_ack"
	// Short aliases make the terminal frame vocabulary convenient to callers.
	FrameTypeComplete = FrameTypeCommandComplete
	FrameTypeLost     = FrameTypeCommandLost
)

// ControlFrame is one length-prefixed JSON control record. The generation is
// mandatory on every frame so a reused process or stale output cannot satisfy
// a current session handshake.
type ControlFrame struct {
	Version    uint16    `json:"version"`
	Type       FrameType `json:"type"`
	SessionID  string    `json:"session_id"`
	CommandID  string    `json:"command_id,omitempty"`
	Generation string    `json:"generation"`
	ExitCode   *int      `json:"exit_code,omitempty"`
}

// ReservedDescriptors identifies the descriptors inherited by an agent. The
// control pair is validated before a shell is started and cannot collide with
// the three ordinary user-facing standard streams.
type ReservedDescriptors struct {
	ControlReadFD  int
	ControlWriteFD int
}

// DefaultReservedDescriptors returns the P030 descriptor allocation.
func DefaultReservedDescriptors() ReservedDescriptors {
	return ReservedDescriptors{ControlReadFD: ReservedControlReadFD, ControlWriteFD: ReservedControlWriteFD}
}

// Validate checks that reserved control descriptors are distinct and outside
// stdin/stdout/stderr. It does not claim OS-level isolation from same-account
// user code; later agent phases protect the shell contract around them.
func (d ReservedDescriptors) Validate() error {
	if d.ControlReadFD < 3 || d.ControlWriteFD < 3 || d.ControlReadFD == d.ControlWriteFD {
		return fmt.Errorf("%w: read=%d write=%d", ErrReservedDescriptor, d.ControlReadFD, d.ControlWriteFD)
	}
	return nil
}

// NewHandshakeFrame constructs the ready handshake emitted by an agent after
// it has bound its persistent shell to one session and generation.
func NewHandshakeFrame(sessionID, generation string) (ControlFrame, error) {
	frame := ControlFrame{Version: ControlProtocolVersion, Type: FrameTypeReady, SessionID: sessionID, Generation: generation}
	if err := frame.Validate(); err != nil {
		return ControlFrame{}, err
	}
	return frame, nil
}

// Validate checks syntax and frame-specific required fields. It intentionally
// does not compare a frame with a caller's expected generation; ControlParser
// performs that authority-bound check.
func (f ControlFrame) Validate() error {
	if f.Version != ControlProtocolVersion {
		return fmt.Errorf("%w: version %d", ErrControlProtocol, f.Version)
	}
	if !validFrameType(f.Type) {
		return fmt.Errorf("%w: type %q", ErrControlFrameMalformed, f.Type)
	}
	if err := validateControlValue("session_id", f.SessionID, 256); err != nil {
		return err
	}
	if err := validateControlValue("generation", f.Generation, 256); err != nil {
		return err
	}
	if f.CommandID != "" {
		if err := validateControlValue("command_id", f.CommandID, 256); err != nil {
			return err
		}
	}
	switch f.Type {
	case FrameTypeReady, FrameTypeHello:
		if f.CommandID != "" || f.ExitCode != nil {
			return fmt.Errorf("%w: handshake carries command result", ErrControlHandshake)
		}
	case FrameTypeCommandStarted:
		if f.CommandID == "" || f.ExitCode != nil {
			return fmt.Errorf("%w: command_started fields", ErrControlFrameMalformed)
		}
	case FrameTypeCommandComplete, FrameTypeCommandLost:
		if f.CommandID == "" || f.ExitCode == nil {
			return fmt.Errorf("%w: terminal command fields", ErrControlFrameMalformed)
		}
	case FrameTypeStopAck:
		if f.CommandID != "" || f.ExitCode != nil {
			return fmt.Errorf("%w: stop acknowledgement fields", ErrControlFrameMalformed)
		}
	}
	if f.ExitCode != nil && (*f.ExitCode < -255 || *f.ExitCode > 255) {
		return fmt.Errorf("%w: exit code %d", ErrControlFrameMalformed, *f.ExitCode)
	}
	return nil
}

// WriteControlFrame writes one bounded big-endian length-prefixed JSON frame.
func WriteControlFrame(w io.Writer, frame ControlFrame) error {
	if w == nil {
		return fmt.Errorf("%w: nil writer", ErrControlProtocol)
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrControlFrameMalformed, err)
	}
	if uint32(len(body)) > MaxControlFrameBytes {
		return ErrControlFrameTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := writeFull(w, header[:]); err != nil {
		return fmt.Errorf("%w: write length: %v", ErrControlProtocol, err)
	}
	if _, err := writeFull(w, body); err != nil {
		return fmt.Errorf("%w: write body: %v", ErrControlProtocol, err)
	}
	return nil
}

// ReadControlFrame reads and validates one bounded frame. The length is
// checked before allocation, so fuzzed or hostile prefixes cannot request an
// unbounded body.
func ReadControlFrame(r io.Reader) (ControlFrame, error) {
	if r == nil {
		return ControlFrame{}, fmt.Errorf("%w: nil reader", ErrControlProtocol)
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return ControlFrame{}, fmt.Errorf("%w: read length: %v", ErrControlProtocol, err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return ControlFrame{}, fmt.Errorf("%w: zero length", ErrControlFrameMalformed)
	}
	if length > MaxControlFrameBytes {
		return ControlFrame{}, ErrControlFrameTooLarge
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(r, body); err != nil {
		return ControlFrame{}, fmt.Errorf("%w: read body: %v", ErrControlProtocol, err)
	}
	var frame ControlFrame
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return ControlFrame{}, fmt.Errorf("%w: decode: %v", ErrControlFrameMalformed, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ControlFrame{}, fmt.Errorf("%w: trailing JSON", ErrControlFrameMalformed)
	}
	if err := frame.Validate(); err != nil {
		return ControlFrame{}, err
	}
	return frame, nil
}

// ControlParser binds frame parsing to one immutable session/generation. A
// printed marker on stdout is not supplied to this reader and cannot spoof a
// command completion frame.
type ControlParser struct {
	sessionID  string
	generation string
}

// NewControlParser validates the authority-bound identity used for parsing.
func NewControlParser(sessionID, generation string) (*ControlParser, error) {
	if err := validateControlValue("session_id", sessionID, 256); err != nil {
		return nil, err
	}
	if err := validateControlValue("generation", generation, 256); err != nil {
		return nil, err
	}
	return &ControlParser{sessionID: sessionID, generation: generation}, nil
}

// Read reads one frame and rejects a stale session or generation before the
// caller can treat it as a handshake or command boundary.
func (p *ControlParser) Read(r io.Reader) (ControlFrame, error) {
	if p == nil {
		return ControlFrame{}, fmt.Errorf("%w: nil parser", ErrControlProtocol)
	}
	frame, err := ReadControlFrame(r)
	if err != nil {
		return ControlFrame{}, err
	}
	if frame.SessionID != p.sessionID {
		return ControlFrame{}, ErrControlSession
	}
	if frame.Generation != p.generation {
		return ControlFrame{}, ErrControlGeneration
	}
	return frame, nil
}

// ReadHandshake reads the first ready frame and enforces the handshake type.
func (p *ControlParser) ReadHandshake(r io.Reader) (ControlFrame, error) {
	frame, err := p.Read(r)
	if err != nil {
		return ControlFrame{}, err
	}
	if frame.Type != FrameTypeReady {
		return ControlFrame{}, ErrControlHandshake
	}
	return frame, nil
}

func validFrameType(value FrameType) bool {
	switch value {
	case FrameTypeHello, FrameTypeReady, FrameTypeCommandStarted, FrameTypeCommandComplete, FrameTypeCommandLost, FrameTypeStopAck:
		return true
	default:
		return false
	}
}

func validateControlValue(name, value string, max int) error {
	if value == "" || len(value) > max || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s", ErrControlFrameMalformed, name)
	}
	return nil
}

func writeFull(w io.Writer, data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		n, err := w.Write(data)
		total += n
		data = data[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
